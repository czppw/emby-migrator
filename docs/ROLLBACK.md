# Rollback Guide

本文用于 Emby Migrator 出现问题时快速回滚到已验收版本。不要在本文记录服务器地址、SSH 用户名、私钥路径、API Key 或 token。

## 1. 当前首选回滚基线

- Docker 镜像：`czppwa/emby-migrator:sha-8c42aed`
- Git commit：`8c42aed2ee34524165c9c69f2cbee5832de38c96`
- 说明：修复 Emby 4.9.5 人物头像查找后的验收基线。
- Docker Hub `latest` 在记录时仍对应该功能基线，但实际回滚必须使用 `sha-8c42aed`，不要依赖 `latest`。
- 机器可读记录：`docs/release-baseline.json`

## 2. 回滚前检查

回滚前先确认：

1. 当前问题是否影响导出/导入核心流程。
2. 当前容器名称。
3. 当前挂载目录。
4. 是否需要保留 `/opt/emby-migrator/data` 中已有导出包和报告。
5. 是否需要保存当前失败版本的完整日志。

推荐先保存日志和容器信息：

```bash
docker ps -a --filter name=emby-migrator
docker logs emby-migrator > emby-migrator-before-rollback.log 2>&1
```

## 3. 标准回滚命令

保留宿主机数据目录和配置目录，只替换容器镜像：

```bash
docker pull czppwa/emby-migrator:sha-8c42aed

docker rm -f emby-migrator

docker run -d \
  --name emby-migrator \
  --restart unless-stopped \
  --network host \
  -e TZ=Asia/Shanghai \
  -e EMBY_MIGRATOR_PASSWORD=password \
  -v /opt/emby-migrator/data:/data \
  -v /opt/emby-migrator/config:/config \
  czppwa/emby-migrator:sha-8c42aed
```

打开页面：

```text
http://服务器IP:8787
```

## 4. 不覆盖数据的原则

默认回滚只删除并重建容器，不删除宿主机目录：

- `/opt/emby-migrator/data`
- `/opt/emby-migrator/config`

不要为了回滚删除这些目录，除非用户明确要求清空数据。

注意：回滚 migrator 容器无法撤销已经写入目标 Emby 的元数据、媒体图片或人物头像。远端导入测试必须使用可重建测试 Emby，或在导入前备份目标 Emby 数据目录、配置和数据库；如果目标 Emby 已被错误写入，只能通过目标 Emby 备份或快照恢复。

## 5. 回滚后验证

回滚后至少验证：

1. 页面能打开。
2. 默认密码或用户自定义密码能登录。
3. 页面版本号正常显示。
4. 能连接 Emby。
5. 能读取媒体库。
6. 能识别已有导出包。
7. 使用小样本或已知导出包执行一次导入验证。
8. 日志能实时输出，完整日志可下载。

如果是因为新版本导入失败而回滚，应使用同一个导出包在回滚版本上复测关键路径。

## 6. 回滚结果记录

回滚完成后记录：

- 回滚时间。
- 回滚原因。
- 问题版本镜像标签。
- 回滚目标镜像标签。
- 使用的导出包。
- 回滚后验证结果。
- 是否需要继续修复新版本。

不要记录 API Key、token、SSH 信息。

## 7. 更新回滚基线

当新版本通过用户验收后：

1. 在 `docs/PROJECT_BLUEPRINT.md` 的“已知回滚点”更新当前验收基线。
2. 在本文第 1 节更新首选回滚镜像和 Git commit。
3. 在 `docs/release-baseline.json` 更新机器可读基线。
4. 在远端测试报告中记录新基线。
5. 使用 `[skip ci]` 提交纯文档更新，避免不必要地覆盖 Docker Hub `latest`。
