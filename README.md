# Emby Migrator

轻量 Docker Web 工具，用于通过 Emby API 导出和导入 Emby 元数据、项目图片和人物头像。

本项目逻辑以现有 Python 脚本为基线：导出时保存 `manifest.json`、`info.json`、`raw.json` 和图片；导入时不使用旧 Emby 内部 ID 做跨服务器匹配，而是优先使用媒体文件名、ProviderIds、剧集信息、名称和 OriginalTitle。

## 默认部署约定

- Web 端口：`8787`
- 默认登录密码：`password`
- 容器内数据目录：`/data`
- 容器内导出包目录：`/data/exports`
- 推荐宿主机数据目录：`/opt/emby-migrator/data`
- 推荐宿主机配置目录：`/opt/emby-migrator/config`
- 宿主机实际导出位置：`/opt/emby-migrator/data/exports`

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
  -p 8787:8787 \
  -e TZ=Asia/Shanghai \
  -e EMBY_MIGRATOR_PASSWORD=password \
  -v /opt/emby-migrator/data:/data \
  -v /opt/emby-migrator/config:/config \
  <dockerhub-username>/emby-migrator:latest
```

打开：

```text
http://服务器IP:8787
```

导出完成后，导出包会在宿主机：

```text
/opt/emby-migrator/data/exports
```

## Docker Compose

```yaml
services:
  emby-migrator:
    image: <dockerhub-username>/emby-migrator:latest
    container_name: emby-migrator
    ports:
      - "8787:8787"
    environment:
      TZ: Asia/Shanghai
      EMBY_MIGRATOR_PASSWORD: password
    volumes:
      - /opt/emby-migrator/data:/data
      - /opt/emby-migrator/config:/config
    restart: unless-stopped
```

## GitHub 自动发布到 Docker Hub

仓库已包含 `.github/workflows/dockerhub.yml`。推送到 `main` 或推送 `v*` 标签时，会自动构建并推送：

- `DOCKERHUB_USERNAME/emby-migrator:latest`
- `DOCKERHUB_USERNAME/emby-migrator:vX.Y.Z`
- `DOCKERHUB_USERNAME/emby-migrator:sha-xxxxxxx`

在 GitHub 仓库的 `Settings -> Secrets and variables -> Actions` 添加：

- `DOCKERHUB_USERNAME`：Docker Hub 用户名
- `DOCKERHUB_TOKEN`：Docker Hub Access Token

未配置这两个 secrets 时，GitHub Actions 会跳过镜像推送，不会把 workflow 标红。

Docker Hub 里建议提前创建仓库：

```text
emby-migrator
```

## 连接 Emby 注意事项

容器内的 `localhost` 指容器自身。访问宿主机 Emby 时：

- Windows/macOS Docker Desktop 通常可用 `host.docker.internal`
- Linux 推荐使用宿主机局域网 IP，例如 `http://192.168.1.10:8096`
- 如果 Emby 在另一台服务器，直接填远程服务器地址和端口

## 本地开发

```bash
go test ./...
go run ./cmd/server
```

然后打开：

```text
http://localhost:8787
```

## 安全说明

- 默认密码是 `password`，公开部署后建议改为更强密码。
- API Key 只通过页面请求发送给后端，不写入代码或镜像。
- 日志会尽量避免打印完整 API Key。
- 不直接读写 Emby 数据库文件，所有操作都通过 Emby API 完成。
- 可选设置 `EMBY_MIGRATOR_SESSION_SECRET`，用于固定登录 Cookie 签名密钥；不设置时每次启动会自动生成临时密钥。
