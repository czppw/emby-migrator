# Emby Migrator

轻量 Docker Web 工具，用于通过 Emby API 导出和导入 Emby 元数据、项目图片和人物头像。

本项目逻辑以现有 Python 脚本为基线：导出时保存 `manifest.json`、`info.json`、`raw.json` 和图片；导入时不使用旧 Emby 内部 ID 做跨服务器匹配，而是优先使用媒体文件名、ProviderIds、剧集信息、名称和 OriginalTitle。

当前产品方向是单用户自托管工具：页面只提供一个访问密码入口，不提供额外账号体系。

## 默认部署约定

- Web 端口：`8787`
- 默认登录密码：`password`
- 容器内数据目录：`/data`
- 容器内导出包目录：`/data/exports`
- 推荐宿主机数据目录：`/opt/emby-migrator/data`
- 推荐宿主机配置目录：`/opt/emby-migrator/config`
- 宿主机实际导出位置：`/opt/emby-migrator/data/exports`
- 应用配置文件：`/config/settings.json`
- TG 配置文件：`/config/telegram.json`

## Docker Run

先创建宿主机目录：

```bash
mkdir -p /opt/emby-migrator/data /opt/emby-migrator/config
```

运行容器：

```bash
docker run -d \
  --name emby-migrator \
  --restart unless-stopped \
  --network host \
  -e TZ=Asia/Shanghai \
  -e EMBY_MIGRATOR_PASSWORD=password \
  -v /opt/emby-migrator/data:/data \
  -v /opt/emby-migrator/config:/config \
  czppwa/emby-migrator:latest
```

打开：

```text
http://服务器IP:8787
```

检查服务是否启动：

```bash
curl http://服务器IP:8787/api/health
```

镜像也内置 Docker `HEALTHCHECK`，容器启动后可用 `docker ps` 查看健康状态。

默认使用 host 网络模式，容器内访问 `127.0.0.1` 就是宿主机本机，方便连接本机 Emby、代理或反向代理。host 模式下不需要 `-p` 端口映射；如果宿主机 `8787` 已被占用，可以增加 `-e EMBY_MIGRATOR_ADDR=:8788` 改端口。

导出完成后，导出包会在宿主机：

```text
/opt/emby-migrator/data/exports
```

镜像默认以容器 root 用户运行，方便直接写入宿主机挂载目录。如果自行指定 `--user`，需要确保该用户对宿主机数据目录有写权限；如果宿主机挂载目录不可写，导出时会出现 `mkdir /data/exports: permission denied`。

## Docker Compose

```yaml
services:
  emby-migrator:
    image: czppwa/emby-migrator:latest
    container_name: emby-migrator
    network_mode: host
    environment:
      TZ: Asia/Shanghai
      EMBY_MIGRATOR_PASSWORD: password
    volumes:
      - /opt/emby-migrator/data:/data
      - /opt/emby-migrator/config:/config
    restart: unless-stopped
```

## 服务器地址簿与 TG

- 应用会把服务器地址簿、当前源/目标服务器选择、默认并发和图片类型保存到 `/config/settings.json`。
- 页面支持保存多个服务器地址簿条目；导出前从源服务器读取媒体库，导入或预检前从目标服务器读取媒体库；API 响应只返回 API Key 掩码，不回传明文 Key。
- TG 设置保存在 `/config/telegram.json`，支持中文测试消息，以及任务 `done` / `failed` / `stopped` 的中文终态通知；导出通知显示源服务器，导入和预检通知显示源服务器与目标服务器。
- TG 代理地址只用于访问 TG，不影响 Emby 连接。

## 连接 Emby 注意事项

默认 host 网络模式下，容器内的 `localhost` / `127.0.0.1` 指宿主机本机。访问本机 Emby、代理或反向代理时可以直接填写本机地址，例如：

```text
http://127.0.0.1:8096
```

如果 Emby 在另一台服务器，直接填远程服务器地址和端口。

## 当前能力状态

- 已完成并已做远端双版本验收：Emby 4.8.11 / 4.9.5 的基础导出、dry-run、实际导入，以及 4.8 / 4.9 之间的跨版本导入。
- 已完成：单用户密码入口、服务器地址簿、源/目标服务器媒体库读取、host 模式部署、TG 配置保存/中文测试消息/中文任务终态通知。
- 部分完成：更结构化的失败报告。当前 `import-report-*.json` 已包含匹配/未匹配/歧义/错误摘要、兼容信息和图片失败统计，但完整失败分组、风险等级、独立失败清单仍待增强。
- 已远端验证基础路径：增量导出、导入增量包、以及 `resume` 根据上次导入报告跳过已成功项目。完整状态级断点恢复仍未完成，不能把进行中的单个图片或头像上传恢复到精确位置。

## 本地开发

```bash
go test ./...
go run ./cmd/server
```

然后打开：

```text
http://localhost:8787
```

本地健康检查：

```bash
curl http://localhost:8787/api/health
```

Windows 本地长期运行可使用仓库内脚本：

```powershell
powershell -ExecutionPolicy Bypass -File .local-run\start-server.ps1
powershell -ExecutionPolicy Bypass -File .local-run\stop-server.ps1
```

脚本会使用 `127.0.0.1:8787`、`.local-run\data`、`.local-run\config`，并把日志写入 `.local-run\server.err.log` / `.local-run\server.out.log`。如果端口已被其他进程占用，启动脚本会直接报告占用进程；如果当前目录触发 Git dubious ownership，构建本地二进制可使用：

```powershell
go build -buildvcs=false -o server.exe ./cmd/server
```

## 安全说明

- 默认密码是 `password`，公开部署后建议改为更强密码。
- API Key 只通过页面请求发送给后端，不写入代码或镜像。
- 日志会尽量避免打印完整 API Key。
- 不直接读写 Emby 数据库文件，所有操作都通过 Emby API 完成。
- 可选设置 `EMBY_MIGRATOR_SESSION_SECRET`，用于固定登录 Cookie 签名密钥；不设置时每次启动会自动生成临时密钥。
