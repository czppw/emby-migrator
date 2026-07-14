package web

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"emby-migrator/internal/dockerengine"
)

const databaseDiscoveryMaxDepth = 6

type embyDatabaseCandidate struct {
	Path          string `json:"path"`
	ContainerName string `json:"containerName,omitempty"`
	Matched       bool   `json:"matched"`
	absolutePath  string
}

type embyDatabaseDiscoveryResponse struct {
	DockerAvailable bool                    `json:"dockerAvailable"`
	Databases       []embyDatabaseCandidate `json:"databases"`
	SelectedPath    string                  `json:"selectedPath,omitempty"`
	ContainerName   string                  `json:"containerName,omitempty"`
}

func (s *Server) handleEmbyDatabaseDiscovery(w http.ResponseWriter, r *http.Request) {
	candidates, err := discoverEmbyDatabases(s.cfg.EmbyDatabaseRoot)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	profile, err := s.mediaDatabaseTargetProfile(r.URL.Query().Get("profileId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	containerName := strings.TrimSpace(r.URL.Query().Get("containerName"))
	if containerName == "" {
		containerName = strings.TrimSpace(profile.ContainerName)
	}
	response := embyDatabaseDiscoveryResponse{
		Databases:     candidates,
		ContainerName: containerName,
	}

	dockerCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	dockerErr := s.docker.Ping(dockerCtx)
	cancel()
	response.DockerAvailable = dockerErr == nil
	if response.DockerAvailable && containerName != "" {
		targetCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		target, targetErr := s.docker.Inspect(targetCtx, containerName)
		cancel()
		hostname, hostErr := os.Hostname()
		if targetErr == nil && hostErr == nil {
			selfCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			self, selfErr := s.docker.Inspect(selfCtx, hostname)
			cancel()
			if selfErr == nil {
				matchDatabaseMounts(response.Databases, target.Mounts, self.Mounts, containerName)
			}
		}
	}

	savedPath := strings.TrimSpace(profile.DatabasePath)
	if candidateExists(response.Databases, savedPath) {
		response.SelectedPath = savedPath
	} else {
		for _, candidate := range response.Databases {
			if candidate.Matched {
				response.SelectedPath = candidate.Path
				break
			}
		}
		if response.SelectedPath == "" && len(response.Databases) == 1 {
			response.SelectedPath = response.Databases[0].Path
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func discoverEmbyDatabases(root string) ([]embyDatabaseCandidate, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("媒体数据库功能未启用：请设置 EMBY_MIGRATOR_EMBY_DB_ROOT")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("解析媒体数据库根目录失败：%w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("媒体数据库根目录不可用：%w", err)
	}
	candidates := make([]embyDatabaseCandidate, 0)
	err = filepath.WalkDir(resolvedRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(resolvedRoot, path)
		if err != nil {
			return err
		}
		depth := pathDepth(relative)
		if entry.IsDir() && depth > databaseDiscoveryMaxDepth {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !strings.EqualFold(entry.Name(), "library.db") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		resolvedPath, err := filepath.EvalSymlinks(path)
		if err != nil || !pathInside(resolvedRoot, resolvedPath) {
			return nil
		}
		candidates = append(candidates, embyDatabaseCandidate{
			Path:         filepath.ToSlash(relative),
			absolutePath: resolvedPath,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("扫描 Emby 数据库失败：%w", err)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Path < candidates[j].Path })
	return candidates, nil
}

func matchDatabaseMounts(candidates []embyDatabaseCandidate, targetMounts, selfMounts []dockerengine.Mount, containerName string) {
	for index := range candidates {
		for _, selfMount := range selfMounts {
			if !pathInside(filepath.Clean(selfMount.Destination), candidates[index].absolutePath) {
				continue
			}
			for _, targetMount := range targetMounts {
				if sameMountSource(selfMount.Source, targetMount.Source) {
					candidates[index].Matched = true
					candidates[index].ContainerName = containerName
					break
				}
			}
			if candidates[index].Matched {
				break
			}
		}
	}
}

func candidateExists(candidates []embyDatabaseCandidate, value string) bool {
	value = filepath.ToSlash(filepath.Clean(strings.TrimSpace(value)))
	for _, candidate := range candidates {
		if candidate.Path == value {
			return true
		}
	}
	return false
}

func sameMountSource(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	return a != "." && b != "." && strings.EqualFold(a, b)
}

func pathInside(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathDepth(relative string) int {
	relative = filepath.Clean(relative)
	if relative == "." || relative == "" {
		return 0
	}
	return len(strings.Split(relative, string(filepath.Separator)))
}
