# ADP OpenAI Gateway

[![Go Version](https://img.shields.io/badge/Go-1.21+-blue.svg)](https://golang.org)
[![Release](https://img.shields.io/github/v/release/zhaoyangyang666/adp-openai-gateway)](https://github.com/zhaoyangyang666/adp-openai-gateway/releases)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

将腾讯云 ADP（智能对话平台）转换为 OpenAI 兼容 API 的网关服务，让任何支持 OpenAI API 的客户端都能直接对接腾讯云 AI 服务。

## 功能特性

- ✅ 完全兼容 OpenAI Chat Completions API
- ✅ 支持流式输出（SSE）
- ✅ 支持非流式输出
- ✅ 自动 Token 缓存与刷新
- ✅ WebSocket 连接复用
- ✅ 管理页面在线分配 / 启停 / 删除 API Key，改动即时生效无需重启
- ✅ 对话级动态换模型，同一会话保持上下文
- ✅ systemd 服务管理

## 本 Fork 的改进

本项目基于 [brinkmai/adp-openai-gateway](https://github.com/brinkmai/adp-openai-gateway) 二次开发，主要改进如下：

| 改进项 | 说明 |
|-------|------|
| **API Key 分配管理页面** | 新增 `/admin` 管理页（`ADMIN_PASSWORD` 登录），支持在线生成、启停、删除 API Key，存储迁移至 JSON 文件，改动即时生效无需重启；废弃原 `GATEWAY_KEYS` 静态配置 |
| **对话级动态换模型** | 接入 ADP 新版 HTTP SSE 对话接口，支持在请求 `model` 字段中指定模型，对话中动态切换且同一 session 保持上下文；提供 `GET /v1/models` 查询可用模型清单 |
| **修复 TC3 签名问题** | 修复 TC3-HMAC-SHA256 签名缺少空行导致 `DescribeModelList` 拒签的问题 |
| **内置模型清单更新** | 内置静态模型清单更新为实测可用快照 |
| **API Key 前缀调整** | 网关签发的 API Key 前缀调整为 `sk-gw-` |

## 架构原理

```
┌─────────────────┐      HTTP/SSE      ┌─────────────────┐     WebSocket      ┌─────────────────┐
│  OpenAI 客户端   │ ───────────────── │   Gateway 网关   │ ◀───────────────▶ │  腾讯云 ADP 服务  │
│  (Cursor等)     │                    │   (本项目)       │  Socket.IO v4     │  (LKE 大模型)    │
└─────────────────┘                    └─────────────────┘                    └─────────────────┘
```

## 与第三方客户端集成

在支持自定义 OpenAI API 的客户端中设置：

- **API Base URL**: `http://127.0.0.1:3100/v1`
- **API Key**: 由网关管理页面分配（浏览器打开 `http://<host>:3100/admin`，用 `.env` 的 `ADMIN_PASSWORD` 登录，生成/启停/删除 key，改动即时生效无需重启）
- **模型**: 可通过 `GET /v1/models` 查询可用模型清单（ADP Claw 可换模型白名单 + `adp-default`），在对话请求的 `model` 字段中指定即可动态换模型；`adp-default` / 留空走应用默认模型

支持的客户端包括但不限于：Cursor、Continue、ChatGPT Next Web、LobeChat 等。

## 快速开始

### 前置条件

- Go 1.21+（仅源码编译需要）
- 腾讯云账号（获取 SecretId/SecretKey）
- ADP 智能体应用（获取 BotAppKey）

### 方式一：从源码编译

```bash
git clone https://github.com/zhaoyangyang666/adp-openai-gateway.git
cd adp-openai-gateway

# 编辑配置
cp .env.example .env
vim .env

# 安装并启动
./scripts/install.sh
./scripts/start.sh
```

### 方式二：下载预编译版本

#### 一键安装（推荐）

```bash
# 创建安装目录
mkdir -p ~/adp-openai-gateway && cd ~/adp-openai-gateway

# 第一步：下载文件（首次运行会创建 .env 并退出）
curl -sSL https://raw.githubusercontent.com/zhaoyangyang666/adp-openai-gateway/main/scripts/install-binary.sh | bash

# 第二步：编辑配置
vim .env

# 第三步：完成安装并启动
curl -sSL https://raw.githubusercontent.com/zhaoyangyang666/adp-openai-gateway/main/scripts/install-binary.sh | bash
./scripts/start.sh
```

#### 手动安装

```bash
# Linux x86_64
curl -LO https://github.com/zhaoyangyang666/adp-openai-gateway/releases/latest/download/adp-openai-gateway-linux-amd64
chmod +x adp-openai-gateway-linux-amd64

# 下载配置模板
curl -LO https://raw.githubusercontent.com/zhaoyangyang666/adp-openai-gateway/main/.env.example
cp .env.example .env
vim .env

# 直接运行（前台）
./adp-openai-gateway-linux-amd64

# 或后台运行
nohup ./adp-openai-gateway-linux-amd64 > gateway.log 2>&1 &
```

## 配置说明

| 变量 | 说明 | 必填 |
|-----|------|-----|
| `SECRET_ID` | 腾讯云 SecretId | ✅ |
| `SECRET_KEY` | 腾讯云 SecretKey | ✅ |
| `ADP_BOT_APP_KEY` | ADP 智能体应用 Key | ✅ |
| `GATEWAY_KEYS` | （已废弃）原静态 key 列表；首次运行自动迁移进 gateway-keys.json，请改用管理页面 | 不再生效 |
| `GATEWAY_KEYS_FILE` | key 存储文件路径 | 默认 gateway-keys.json |
| `ADMIN_PASSWORD` | 管理页面（/admin）登录密码；留空则管理页面不可用 | 建议必配 |
| `ADP_SPACE_ID` | ADP 空间 ID，用于拉取 Claw 可用模型白名单 | 留空用内置静态清单 |
| `ADP_REGION` | ADP 管理 API 地域 | 默认 ap-guangzhou |
| `PORT` | 服务端口 | 默认 3100 |
| `HOST` | 监听地址 | 默认 127.0.0.1 |

## API 使用

### 非流式请求

```bash
curl -X POST http://127.0.0.1:3100/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-gw-xxx" \
  -d '{"model":"glm-5","messages":[{"role":"user","content":"你好"}]}'
```

### 流式请求

```bash
curl -X POST http://127.0.0.1:3100/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-gw-xxx" \
  -d '{"model":"glm-5","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

### 查询可用模型

```bash
curl http://127.0.0.1:3100/v1/models \
  -H "Authorization: Bearer sk-gw-xxx"
```

## 服务管理

```bash
# 使用脚本（两种安装方式通用）
./scripts/start.sh    # 启动
./scripts/stop.sh     # 停止
./scripts/restart.sh  # 重启

# 或使用 systemctl
systemctl --user status adp-openai-gateway
systemctl --user restart adp-openai-gateway
journalctl --user -u adp-openai-gateway -f
```

## 致谢

- 上游项目：[brinkmai/adp-openai-gateway](https://github.com/brinkmai/adp-openai-gateway)，本项目在其基础上二次开发

## License

[MIT License](LICENSE)
