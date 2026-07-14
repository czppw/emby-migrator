# Emby Migrator v1.1.0

`v1.1.0` 增加无插件的媒体技术信息迁移能力，并完善数据库发现、Emby 容器自动停启、双版本保护和管理页面布局。

## 新增功能

- 导出 `MediaSources`、`MediaStreams` 和 `Chapters`。
- 在线导入完成目标项目匹配，并生成独立的媒体数据库应用计划。
- 离线写入目标 Emby `library.db`，写入前自动创建 SQLite 一致性备份。
- 支持通过 Docker Socket 自动停止目标 Emby、应用计划、重新启动并执行 API 回读验证。
- 自动扫描挂载到 `/emby-dbs` 的 Emby 数据库，并根据 Docker 挂载关系识别目标实例和容器名。
- 数据库路径限制、SQLite schema 校验、事务写入、`integrity_check` 和错误重启保护。

## 兼容范围

- 媒体技术信息支持 `Emby 4.8.11.x -> 4.8.11.x`。
- 媒体技术信息支持 `Emby 4.9.5.x -> 4.9.5.x`。
- 媒体技术信息不支持 `4.8` 与 `4.9` 之间跨版本数据库写入，程序会在写库前拒绝。
- 普通元数据、媒体图片、人物和人物头像仍通过 Emby API 导入，继续保留已验证的跨版本迁移能力。

## 界面与体验

- 新增目标数据库、Emby 容器和自动停启设置。
- “服务器与媒体恢复”调整为服务器选择与数据库控制两行布局，改善宽屏和移动端显示。
- 版本号、CSS/JavaScript 缓存参数和 README 图文统一更新为 `1.1.0`。
- README 增加媒体技术信息部署、操作步骤、风险边界和 Docker Socket 安全说明。

## 部署变化

普通元数据和图片迁移仍只需挂载 `/data` 与 `/config`。启用媒体技术信息恢复时，额外增加：

```bash
-e EMBY_MIGRATOR_EMBY_DB_ROOT=/emby-dbs \
-e EMBY_MIGRATOR_DOCKER_HOST=unix:///var/run/docker.sock \
-v /path/to/emby/config:/emby-dbs/default \
-v /var/run/docker.sock:/var/run/docker.sock
```

Docker Socket 拥有宿主机 Docker 管理权限，只应在可信的单用户环境中挂载。

## 验证结果

- Emby `4.8.11.0` 最终镜像实测：自动停启、数据库发现、1 个项目、3 条媒体流、2 个章节和 API 回读全部通过。
- Emby `4.9.5.0` 最终镜像实测：自动停启、数据库发现、1 个项目、3 条媒体流、2 个章节和 API 回读全部通过。
- `go test ./...`、`go vet ./...`、干净 Docker 构建、登录与健康检查、依赖校验和公开文件敏感信息扫描全部通过。

完整测试记录见 [`docs/PRE_RELEASE_TEST_REPORT_20260714.md`](PRE_RELEASE_TEST_REPORT_20260714.md)。
