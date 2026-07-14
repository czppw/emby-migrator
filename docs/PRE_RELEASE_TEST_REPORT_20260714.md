# Emby Migrator 1.1.0 发布前测试报告

测试日期：2026-07-14（Asia/Shanghai）

## 验收范围

- 普通元数据、媒体图片、人物与人物头像迁移回归。
- 媒体技术信息导出、在线匹配计划、离线 SQLite 应用、容器自动停启和重启后 API 回读。
- Emby `4.8.11.0 -> 4.8.11.0` 与 `4.9.5.0 -> 4.9.5.0`。
- Docker 干净环境构建、健康检查、登录和前端静态资源版本。

## 本地自动化

| 检查 | 结果 |
| --- | --- |
| `go test -count=1 -buildvcs=false ./...` | 通过 |
| `go vet -buildvcs=false ./...` | 通过 |
| `go build -buildvcs=false ./cmd/server` | 通过 |
| `node --check web/assets/app.js` | 通过 |
| `go mod verify` / `go mod tidy -diff` | 通过 |
| `git diff --check` | 通过 |
| 公开文件敏感信息扫描 | 通过 |

## Docker 验证

- 使用仅包含 Dockerfile、Go 源码、Web 资源和许可证的干净构建上下文。
- Linux 镜像构建成功，镜像大小约 18.7 MB。
- `/api/health` 返回 `ok`，工具版本为 `1.1.0`。
- 默认单用户登录接口正常。
- 首页版本、CSS 和 JavaScript 缓存参数均为 `1.1.0`。
- 构建上下文通过 `.dockerignore` 排除私有目录、测试产物、日志和 PEM 文件。

## 双版本实际恢复

| 目标版本 | 数据库发现 | 自动停启 | 项目 | MediaStreams | Chapters | API 回读 |
| --- | --- | --- | ---: | ---: | ---: | --- |
| Emby 4.8.11.0 | 通过 | 通过 | 1 | 3 | 2 | 通过 |
| Emby 4.9.5.0 | 通过 | 通过 | 1 | 3 | 2 | 通过 |

两次测试均直接使用最终 Docker 镜像执行。应用任务完成后，目标 Emby 容器正常启动，数据库完整性与 API 回读结果一致。

## 兼容边界

- 普通元数据、图片和人物头像仍通过 Emby API 迁移，可继续支持已验证的跨版本路径。
- 媒体技术信息数据库写入只支持 `4.8.11.x -> 4.8.11.x` 和 `4.9.5.x -> 4.9.5.x`。
- 媒体技术信息跨版本写入会被拒绝，不会尝试套用另一版本的 SQLite schema。
- 自动停启需要挂载目标 Emby 配置目录和 Docker Socket；不挂载 Docker Socket 时只能使用手动停启流程。

## 结论

`1.1.0` 发布候选已通过本地自动化、干净 Docker 构建、容器 smoke 和 Emby 4.8.11/4.9.5 双版本实际恢复测试。当前测试证据满足进入发布审核的条件。
