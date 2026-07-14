package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"emby-migrator/internal/dockerengine"
	"emby-migrator/internal/emby"
	"emby-migrator/internal/exporter"
	"emby-migrator/internal/job"
)

const (
	containerStopTimeoutSeconds = 60
	containerTransitionTimeout  = 90 * time.Second
	embyRestartReadyTimeout     = 90 * time.Second
)

type dockerController interface {
	Ping(context.Context) error
	Inspect(context.Context, string) (dockerengine.Container, error)
	Stop(context.Context, string, int) error
	Start(context.Context, string) error
	WaitRunning(context.Context, string) error
	WaitStopped(context.Context, string) error
}

type unavailableDockerController struct {
	err error
}

func (c unavailableDockerController) Ping(context.Context) error {
	return c.err
}

func (c unavailableDockerController) Inspect(context.Context, string) (dockerengine.Container, error) {
	return dockerengine.Container{}, c.err
}

func (c unavailableDockerController) Stop(context.Context, string, int) error { return c.err }
func (c unavailableDockerController) Start(context.Context, string) error     { return c.err }
func (c unavailableDockerController) WaitRunning(context.Context, string) error {
	return c.err
}
func (c unavailableDockerController) WaitStopped(context.Context, string) error {
	return c.err
}

func newDockerController(host string) dockerController {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	if !strings.HasPrefix(host, "unix://") {
		return unavailableDockerController{err: fmt.Errorf("仅支持 unix:// Docker Socket：%s", host)}
	}
	socketPath := strings.TrimSpace(strings.TrimPrefix(host, "unix://"))
	if socketPath == "" {
		return unavailableDockerController{err: fmt.Errorf("Docker Socket 路径为空")}
	}
	return dockerengine.NewClient(socketPath)
}

type mediaDatabaseContainerResult struct {
	Enabled       bool   `json:"enabled"`
	ContainerName string `json:"containerName,omitempty"`
	WasRunning    bool   `json:"wasRunning"`
	Stopped       bool   `json:"stopped"`
	Restarted     bool   `json:"restarted"`
}

type mediaDatabaseJobResult struct {
	exporter.MediaDatabaseApplyResult
	Container    mediaDatabaseContainerResult        `json:"container"`
	Verification *exporter.MediaDatabaseVerifyResult `json:"verification,omitempty"`
}

func (s *Server) mediaDatabaseTargetProfile(profileID string) (appServerProfileSettings, error) {
	settings, err := s.loadAppSettings()
	if err != nil {
		return appServerProfileSettings{}, err
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		profileID = strings.TrimSpace(settings.CurrentTarget)
	}
	if profileID == "" {
		return appServerProfileSettings{}, nil
	}
	profile, ok := findProfileByID(settings.Profiles, profileID)
	if !ok {
		return appServerProfileSettings{}, fmt.Errorf("目标服务器档案不存在，请重新选择导入服务器")
	}
	return profile, nil
}

func (s *Server) applyMediaDatabaseJob(j *job.Job, exportPath, databasePath string, overwrite bool, profile appServerProfileSettings) (result mediaDatabaseJobResult, err error) {
	apply := func() (exporter.MediaDatabaseApplyResult, error) {
		return s.exporter.ApplyMediaDatabasePlan(j.Context(), j, exporter.MediaDatabaseApplyRequest{
			ExportPath:   exportPath,
			DatabasePath: databasePath,
			Overwrite:    overwrite,
		})
	}

	containerName := strings.TrimSpace(profile.ContainerName)
	result.Container = mediaDatabaseContainerResult{
		Enabled:       profile.AutoManageContainer,
		ContainerName: containerName,
	}
	if !profile.AutoManageContainer {
		result.MediaDatabaseApplyResult, err = apply()
		return result, err
	}
	if containerName == "" {
		return result, fmt.Errorf("目标服务器档案未绑定 Emby 容器")
	}
	if hostname, hostErr := os.Hostname(); hostErr == nil && strings.EqualFold(strings.TrimSpace(hostname), containerName) {
		return result, fmt.Errorf("拒绝停止 Emby Migrator 自身容器")
	}
	if err := s.docker.Ping(j.Context()); err != nil {
		return result, fmt.Errorf("无法连接 Docker Engine，请挂载 /var/run/docker.sock：%w", err)
	}
	container, err := s.docker.Inspect(j.Context(), containerName)
	if err != nil {
		return result, fmt.Errorf("读取目标 Emby 容器状态失败：%w", err)
	}
	if hostname, hostErr := os.Hostname(); hostErr == nil && sameContainerID(container.ID, hostname) {
		return result, fmt.Errorf("拒绝停止 Emby Migrator 自身容器")
	}
	result.Container.WasRunning = container.Running
	restartNeeded := false
	defer func() {
		if !restartNeeded {
			return
		}
		restartCtx, cancel := context.WithTimeout(context.Background(), containerTransitionTimeout)
		defer cancel()
		j.Log("info", "任务未正常结束，正在恢复启动 Emby 容器：%s", containerName)
		if restartErr := s.startManagedContainer(restartCtx, containerName); restartErr != nil {
			err = errors.Join(err, fmt.Errorf("恢复启动 Emby 容器失败：%w", restartErr))
			return
		}
		result.Container.Restarted = true
	}()

	if container.Running {
		j.Log("info", "正在停止目标 Emby 容器：%s", containerName)
		restartNeeded = true
		if err := s.docker.Stop(j.Context(), containerName, containerStopTimeoutSeconds); err != nil {
			return result, fmt.Errorf("停止目标 Emby 容器失败：%w", err)
		}
		stopCtx, cancel := context.WithTimeout(j.Context(), containerTransitionTimeout)
		err = s.docker.WaitStopped(stopCtx, containerName)
		cancel()
		if err != nil {
			return result, fmt.Errorf("等待目标 Emby 容器停止失败：%w", err)
		}
		result.Container.Stopped = true
		j.Log("info", "目标 Emby 容器已停止，开始写入数据库")
	} else {
		j.Log("info", "目标 Emby 容器当前已停止，将保持原状态")
	}

	result.MediaDatabaseApplyResult, err = apply()
	if err != nil {
		return result, err
	}
	if !container.Running {
		return result, nil
	}

	restartCtx, cancel := context.WithTimeout(context.Background(), containerTransitionTimeout)
	err = s.startManagedContainer(restartCtx, containerName)
	cancel()
	if err != nil {
		return result, fmt.Errorf("启动目标 Emby 容器失败：%w", err)
	}
	restartNeeded = false
	result.Container.Restarted = true
	j.Log("info", "目标 Emby 容器已启动，等待 Emby API 恢复")

	readyCtx, cancel := context.WithTimeout(j.Context(), embyRestartReadyTimeout)
	err = waitForEmbyReady(readyCtx, emby.Connection{BaseURL: profile.BaseURL, APIKey: profile.APIKey})
	cancel()
	if err != nil {
		return result, fmt.Errorf("Emby 容器已启动，但 API 未在限定时间内恢复：%w", err)
	}
	verifyCtx, cancel := context.WithTimeout(j.Context(), embyRestartReadyTimeout)
	verification, verifyErr := s.exporter.VerifyMediaDatabasePlan(verifyCtx, exportPath, emby.Connection{BaseURL: profile.BaseURL, APIKey: profile.APIKey})
	cancel()
	if verifyErr != nil {
		return result, fmt.Errorf("Emby 重启后媒体技术信息回读验证失败：%w", verifyErr)
	}
	result.Verification = &verification
	j.Log("info", "Emby 重启回读验证完成：项目 %d，媒体流 %d，章节 %d", verification.Items, verification.Streams, verification.Chapters)
	return result, nil
}

func sameContainerID(containerID, hostname string) bool {
	containerID = strings.ToLower(strings.TrimSpace(containerID))
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	return containerID != "" && hostname != "" && (strings.HasPrefix(containerID, hostname) || strings.HasPrefix(hostname, containerID))
}

func (s *Server) startManagedContainer(ctx context.Context, containerName string) error {
	if err := s.docker.Start(ctx, containerName); err != nil {
		return err
	}
	return s.docker.WaitRunning(ctx, containerName)
}

func waitForEmbyReady(ctx context.Context, connection emby.Connection) error {
	client, err := emby.NewClient(connection.BaseURL, connection.APIKey)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastErr error
	for {
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, requestErr := client.SystemInfo(requestCtx)
		cancel()
		if requestErr == nil {
			return nil
		}
		lastErr = requestErr
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}
