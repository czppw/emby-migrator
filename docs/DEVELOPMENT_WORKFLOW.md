# Development Workflow

本文定义 Emby Migrator 后续每轮开发的固定流程。用户采用目标模式：Codex 负责开发、测试、远端验证、发布确认和回滚准备，用户只验收最终版本成果。

## 1. 开始前

每轮开始前必须读取：

1. `docs/PROJECT_BLUEPRINT.md`
2. `docs/REMOTE_VERSION_TESTING.md`
3. `docs/ROLLBACK.md`
4. `docs/release-baseline.json`

如果本轮涉及最终交付或大版本兼容测试，还必须使用：

```text
docs/templates/REMOTE_TEST_REPORT_TEMPLATE.md
```

## 2. 需求归类

先把用户需求归入一个或多个类别：

- P0 稳定化：配置、任务控制、导入预检、版本兼容、导出包校验、回归矩阵。
- P1 性能和可靠性：并发、缓存、重试限流、断点、结构化失败报告。
- P2 用户体验：服务器档案、对比报告、任务页面、响应式布局、中文化图片类型、部署提示。
- P3 长期能力：定时、通知、匹配模板、审计模式、回滚辅助、多用户权限。

如果需求会影响导出、导入、匹配、元数据、图片、人物头像，默认按核心迁移逻辑处理，必须执行更完整测试。

## 3. 变更前检查

开始改代码前确认：

- 当前 Git 工作树状态。
- 当前回滚基线。
- 本轮是否改变导出包格式或 manifest schema。
- 本轮是否可能影响 Emby 4.8.11 或 4.9.5。
- 本轮是否可能把旧 Emby 内部 ID 写入导入 payload。
- 本轮是否需要更新蓝图、runbook、回滚手册或测试报告模板。

## 4. 实现原则

- 优先沿用现有 Go 后端和原有模块边界。
- `internal/emby` 只负责 Emby API。
- `internal/exporter` 负责导出/导入流程。
- `internal/storage` 负责导出包、manifest、报告结构。
- `internal/web` 负责 HTTP API、认证、Web 静态资源。
- 前端不做营销页，首页就是工具。
- 不把服务器地址、SSH 用户名、私钥路径、API Key、token 写进仓库。

## 5. 本地验证

每轮代码改动后至少运行：

```bash
go test ./...
node --check web/assets/app.js
go build ./cmd/server
```

如果改前端，还要做浏览器检查：

- 桌面宽度。
- 手机宽度。
- 媒体库较多时布局。
- 日志窗口显示和下载。
- 任务中止按钮。

## 6. 远端实测触发条件

满足任一条件必须远端实测：

- 改了导出/导入流程。
- 改了匹配策略。
- 改了元数据 payload。
- 改了图片或人物头像上传下载。
- 改了并发、重试、超时、断点。
- 改了导出包格式、manifest、report。
- 修复 Emby 4.8/4.9 兼容问题。

远端实测按 `docs/REMOTE_VERSION_TESTING.md` 执行，并填写 `docs/templates/REMOTE_TEST_REPORT_TEMPLATE.md`。

## 7. 发布流程

需要发布镜像时：

1. 本地测试通过。
2. 必要的远端版本实测通过。
3. 提交代码。
4. 推送 GitHub。
5. 等 GitHub Actions 完成。
6. 确认 Docker Hub 出现 `sha-xxxxxxx` 标签。
7. 确认 `latest` 更新时间符合本次发布。
8. 向用户说明测试结果、镜像标签、回滚标签。

纯文档变更使用 `[skip ci]`，避免不必要地覆盖 Docker Hub `latest`。
Docker Hub workflow 已对 `docs/**` 和仓库根目录 Markdown 文件设置路径忽略；纯文档更新不应触发镜像构建。涉及代码、Dockerfile、workflow 或发布逻辑的变更仍需按发布流程确认 Docker Hub 标签。

## 8. 验收前交付物

交给用户验收前至少提供：

- 本轮功能摘要。
- 本地测试结果。
- 远端实测结果，若适用。
- Docker Hub 镜像标签。
- Git commit。
- 已知问题。
- 回滚命令。
- 是否建议验收。

## 9. 回滚准备

每次用户验收通过后：

1. 更新 `docs/PROJECT_BLUEPRINT.md` 的当前验收基线。
2. 更新 `docs/ROLLBACK.md` 的当前首选回滚基线。
3. 更新 `docs/release-baseline.json` 的机器可读基线。
4. 在测试报告中记录新基线。
5. 使用 `[skip ci]` 提交纯文档基线更新。

如果新版本失败，按 `docs/ROLLBACK.md` 回滚到当前验收基线。

## 10. 上下文保留

以下信息出现变化时必须写回文档：

- Emby 版本兼容差异。
- 新的回滚标签。
- 新的远端测试媒体库。
- Docker 部署约定。
- 匹配策略变化。
- 导出包格式变化。
- 已验证的重大 bug 和修复结论。

敏感连接信息只允许保存在本地私密上下文，不进仓库。
