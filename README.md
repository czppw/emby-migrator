![Emby Migrator - Emby 换机不重刮削](docs/assets/readme-hero.svg)

<p align="center">
  <a href="README.en.md">English</a> · 中文
</p>

[![GitHub](https://img.shields.io/badge/GitHub-czppw%2Femby--migrator-111827?style=for-the-badge&logo=github)](https://github.com/czppw/emby-migrator)
[![Docker Hub](https://img.shields.io/badge/Docker%20Hub-czppwa%2Femby--migrator-2496ED?style=for-the-badge&logo=docker&logoColor=white)](https://hub.docker.com/r/czppwa/emby-migrator)
[![Version](https://img.shields.io/github/v/release/czppw/emby-migrator?style=for-the-badge&color=315CF6)](https://github.com/czppw/emby-migrator/releases/latest)
![License](https://img.shields.io/badge/license-AGPL--3.0--or--later-22C55E)

# Emby Migrator

> **Emby 换服务器，不想重新刮削整个媒体库？**
>
> Emby Migrator 是一个轻量级 Docker Web 工具，用于迁移和备份 Emby 媒体库的元数据、海报图片、人物头像及媒体技术信息。

它适合：

- 换服务器或重建 Emby 媒体库；
- 不想重新刮削几百 TB 的媒体库；
- 在多个 Emby 实例之间迁移媒体信息；
- 定期备份元数据、图片和人物头像。

![Emby Migrator 工作流程](docs/assets/readme-workflow.svg)

## 它解决什么问题？

Emby 换机时，文件可以重新入库，但标题、简介、演员、海报、人物头像和媒体信息通常需要重新匹配、重新下载和重新刮削。这既耗时，也可能因为网络、识别结果或命名差异产生偏差。

Emby Migrator 将迁移拆成两个阶段：

```text
旧 Emby
  │ 导出元数据、图片、人物头像和媒体信息
  ▼
可保存、可复制、可检查的迁移包
  │
  ▼
新 Emby 先扫描文件建立媒体条目
  │ 不重新刮削
  ▼
导入并按稳定特征重新匹配
  │
  ▼
恢复元数据、图片、人物头像和可兼容的媒体技术信息
```

**重要：扫描不等于刮削。** 新 Emby 仍然需要扫描文件，建立可供导入的媒体条目；Emby Migrator 主要帮你避免重新刮削和重新下载已有资料。

## 核心能力

| 能力 | 说明 |
| --- | --- |
| 元数据迁移 | 标题、简介、演员、评分等媒体信息导出与导入 |
| 图片迁移 | 主海报、背景图、Logo、横幅、艺术图、缩略图、光盘图等 |
| 人物迁移 | 人物信息和演员头像导出与导入 |
| 媒体技术信息 | 可选迁移编码、分辨率、码率、音轨、字幕、章节等信息 |
| 稳定匹配 | 优先按文件名、ProviderIds、剧集信息等匹配，不依赖旧 Item ID |
| 导入预检 | 正式写入前查看 matched、unmatched、ambiguous 和 error |
| 导入报告 | 保存任务日志、匹配结果、图片统计和失败摘要 |
| 增量导出 | 只处理新增或变化的内容，减少重复工作 |
| Web 操作 | 通过网页管理服务器、导出包、任务和日志 |
| Telegram 通知 | 支持中文测试消息和任务终态通知 |
| Docker 部署 | 单容器运行，数据和配置可独立挂载 |

## 版本兼容矩阵

| 功能 | Emby 4.8.11 → 4.8.11 | Emby 4.9.5 → 4.9.5 | 4.8 ↔ 4.9 |
| --- | :---: | :---: | :---: |
| 元数据 | ✅ | ✅ | ✅ |
| 媒体图片 | ✅ | ✅ | ✅ |
| 人物和演员头像 | ✅ | ✅ | ✅ |
| MediaInfo | ✅ | ✅ | ❌ |
| MediaStreams | ✅ | ✅ | ❌ |
| Chapters | ✅ | ✅ | ❌ |

普通元数据、图片和人物信息通过 Emby API 迁移，可跨版本使用。媒体技术信息需要停服写入目标 `library.db`，仅支持已验证的同系列版本；跨版本会明确拒绝，不会强行写入。

## 推荐迁移流程

### 旧服务器

1. 使用 Emby Migrator 连接旧 Emby。
2. 选择需要迁移的媒体库。
3. 导出元数据、图片和人物头像。
4. 如果需要恢复媒体技术信息，同时导出 MediaInfo、MediaStreams 和 Chapters。
5. 将完整迁移包复制到新服务器。

### 新服务器

1. 安装与旧服务器兼容的 Emby 版本。
2. 创建媒体库并指向媒体文件目录。
3. 关闭自动识别、自动下载图片和实时元数据更新。
4. **只扫描文件，让 Emby 建立媒体条目。**
5. 在 Emby Migrator 中执行导入预检。
6. 确认匹配结果后执行正式导入。
7. 抽查结果，确认无误后再决定是否开启后续自动任务。

### 媒体技术信息恢复

媒体技术信息恢复是可选的两阶段流程：

1. 在线阶段读取目标媒体并生成不可变匹配计划；
2. 停止目标 Emby，备份 `library.db`，校验目标身份和版本后写入数据库；
3. 启动 Emby，并通过 API 回读验证。

不要在未知版本或未确认目标数据库归属时写入 `library.db`。

## 快速部署

### 最小部署

```bash
mkdir -p /opt/emby-migrator/data/imports \
         /opt/emby-migrator/config \
         /opt/emby-migrator/imports

docker run -d \
  --name emby-migrator \
  --restart unless-stopped \
  --network host \
  -e TZ=Asia/Shanghai \
  -e EMBY_MIGRATOR_PASSWORD='请设置一个强密码' \
  -e EMBY_MIGRATOR_IMPORT_ROOT=/imports \
  -v /opt/emby-migrator/data:/data \
  -v /opt/emby-migrator/config:/config \
  -v /opt/emby-migrator/imports:/imports \
  czppwa/emby-migrator:v1.1.6
```

打开：

```text
http://服务器IP:8787
```

导出包目录：

```text
/opt/emby-migrator/data/exports
```

导入包目录：

```text
/opt/emby-migrator/data/imports
```

### 恢复媒体技术信息的额外挂载

如果需要恢复 MediaInfo、MediaStreams 和 Chapters，还需要以读写方式挂载目标 Emby 的 config 目录，并允许工具管理目标容器：

```bash
-e EMBY_MIGRATOR_EMBY_DB_ROOT=/emby-dbs \
-e EMBY_MIGRATOR_DOCKER_HOST=unix:///var/run/docker.sock \
-v /opt/emby/config:/emby-dbs/default \
-v /var/run/docker.sock:/var/run/docker.sock
```

页面会扫描并选择目标 `library.db`。启用自动停启后，程序会在写入前校验目标 ServerID、版本系列、数据库 schema 和项目锚点，并在写入前创建备份。

> `/var/run/docker.sock` 等同于宿主机 Docker 管理权限，只建议在可信的单用户环境中启用。只迁移普通元数据、图片和人物头像时不需要挂载 Docker Socket。

## 页面操作

1. 登录 Web 页面。
2. 添加源 Emby 和目标 Emby 地址及 API Key。
3. 测试连接并保存服务器。
4. 选择源媒体库和导出选项。
5. 启动导出任务，等待迁移包生成。
6. 将完整迁移包复制到新服务器的 `data/imports` 或独立 `imports` 目录。
7. 刷新导入包并执行预检。
8. 确认匹配结果后执行导入。
9. 下载导入报告，查看成功、未匹配、歧义和错误项目。

## 安全说明

- 请不要在公网部署时使用默认密码。
- API Key 只由后端保存，前端配置接口不会回传明文 Key。
- 日志会尽量避免记录完整 API Key。
- 媒体技术信息只在用户明确操作且目标 Emby 已停止时写入数据库。
- 写库前会创建 SQLite 备份，使用事务并执行完整性检查。
- 数据库路径被限制在 `EMBY_MIGRATOR_EMBY_DB_ROOT` 范围内。
- 可以使用 `EMBY_MIGRATOR_SESSION_SECRET` 固定登录 Cookie 签名密钥。

## Docker Compose

```yaml
services:
  emby-migrator:
    image: czppwa/emby-migrator:v1.1.6
    container_name: emby-migrator
    network_mode: host
    environment:
      TZ: Asia/Shanghai
      EMBY_MIGRATOR_PASSWORD: 请设置强密码
      EMBY_MIGRATOR_IMPORT_ROOT: /imports
    volumes:
      - /opt/emby-migrator/data:/data
      - /opt/emby-migrator/config:/config
      - /opt/emby-migrator/imports:/imports
    restart: unless-stopped
```

## 本地开发

```bash
go test ./...
go vet ./...
go run ./cmd/server
```

打开：

```text
http://localhost:8787
```

健康检查：

```bash
curl http://localhost:8787/api/health
```

## 项目边界

Emby Migrator 是按需迁移和备份恢复工具，不是常驻双向同步器。它不负责迁移：

- 播放进度；
- 收藏状态；
- 合集关系；
- Emby 全局设置；
- 新服务器尚未扫描建立的媒体条目。

## 项目链接

- GitHub：<https://github.com/czppw/emby-migrator>
- Docker Hub：<https://hub.docker.com/r/czppwa/emby-migrator>
- 当前版本：`v1.1.6`
- 开源协议：AGPL-3.0-or-later

## 许可证

Emby Migrator 使用 **GNU Affero General Public License v3.0 or later** 授权。Fork、修改版、重新分发副本和对外提供网络访问的部署版本，请保留原始版权声明、NOTICE、项目来源链接和 AGPL 授权条款。
