# WeClaw - 微信公众号 OpenClaw 网关

## 项目概述

WeClaw 是一个 Golang 后端服务，通过微信公众号（订阅号）让普通用户使用 OpenClaw AI 助手。
用户关注订阅号后，系统自动创建一个 Docker 容器运行 OpenClaw，用户通过微信消息与自己的 OpenClaw 实例交互。

## 技术栈

- **语言**: Go 1.25+
- **Web 框架**: Gin
- **ORM**: GORM (SQLite)
- **容器**: Docker SDK for Go
- **配置**: Viper (YAML + 环境变量)
- **日志**: Zap

## 项目结构

```
weclaw/
├── cmd/weclaw/main.go           # 程序入口
├── internal/
│   ├── api/test_api.go          # 测试/调试 API
│   ├── config/config.go         # 配置管理
│   ├── wechat/                  # 微信公众号对接
│   │   ├── handler.go           # HTTP 处理入口
│   │   ├── api.go               # 微信API客户端 (access_token, 客服消息)
│   │   ├── verify.go            # 微信签名校验
│   │   └── message.go           # XML 消息解析/构建
│   ├── container/               # Docker 容器管理
│   │   ├── manager.go           # 容器生命周期管理
│   │   └── pool.go              # 容器池 & 端口分配
│   ├── openclaw/                # OpenClaw 交互
│   │   ├── client.go            # OpenClaw API 客户端
│   │   └── formatter.go         # Markdown → 微信文本转换
│   ├── user/                    # 用户管理
│   │   ├── model.go             # 数据模型
│   │   └── service.go           # 业务逻辑
│   ├── router/router.go         # 消息路由
│   └── store/store.go           # 数据存储 (SQLite)
├── pkg/logger/logger.go         # 日志工具
├── configs/config.yaml          # 默认配置
├── scripts/test.sh              # 功能测试脚本
└── deployments/                 # 部署文件
```

## 核心流程

1. 用户关注 → 创建 Docker 容器 → 回复欢迎消息
2. 用户发消息 → 唤醒容器(如休眠) → 转发到 OpenClaw → 异步回复结果
3. 用户取消关注 → 停止并清理容器

## 开发命令

```bash
# 运行
go run cmd/weclaw/main.go

# 构建
go build -o bin/weclaw cmd/weclaw/main.go

# 测试 (单元测试)
go test ./...

# 功能测试 (需要先启动服务)
./scripts/test.sh          # 基础测试 (健康检查 + 签名验证)
./scripts/test.sh full     # 完整流程 (注册→发消息→清理)
./scripts/test.sh docker   # Docker 连通性
./scripts/test.sh register my-user  # 注册用户
./scripts/test.sh send my-user '你好'  # 发送消息
./scripts/test.sh cleanup my-user   # 清理用户
```

## API 端点

### 微信接口
- `GET  /wechat`          - 微信服务器验证
- `POST /wechat`          - 微信消息/事件接收

### 测试接口 (`/api/test/`)
- `GET  /api/test/docker`        - Docker 连通性测试 (创建测试容器)
- `POST /api/test/register`      - 模拟用户注册 `{"openid":"xxx"}`
- `POST /api/test/send`          - 发送消息 `{"openid":"xxx","message":"hello"}`
- `GET  /api/test/user/:openid`  - 查询用户信息
- `GET  /api/test/users`         - 列出所有用户
- `DELETE /api/test/user/:openid` - 删除用户及容器

### 运维接口
- `GET  /healthz`         - 健康检查

## 配置

配置文件: `configs/config.yaml`，支持 `WECLAW_` 前缀环境变量覆盖。

关键配置:
- `wechat.token` - 微信公众号 Token (自定义暗号，需与公众平台一致)
- `wechat.app_id` / `wechat.app_secret` - 微信 AppID/Secret
- `docker.max_containers` - 最大容器数
- `docker.openclaw_image` - OpenClaw Docker 镜像
- `docker.openclaw_host_data_dir` - 宿主机目录，用于挂载每个容器的 `~/.openclaw`（配置+会话）；Linux 下若容器内报 EACCES，可对该目录执行 `chown -R 1000:1000 <目录>`
- `openclaw.api_key` - LLM API Key
- `openclaw.base_url` - LLM API Base URL (可选，用于自定义API地址/代理)
- `openclaw.model_provider` - LLM 提供者 (anthropic/openai)
- `openclaw.model_name` - 模型名称

## 注意事项

- 微信被动回复有 5 秒超时限制，需用异步方式处理 AI 响应
- 订阅号需要认证才能使用客服消息接口
- 每个 OpenClaw 容器约需 512MB 内存
- 不活跃容器会自动休眠以节省资源
- OpenClaw 容器通过宿主机目录挂载注入 `config.json5`（含 `gateway.bind=lan` 所需配置），Gateway 以 `--bind lan` 启动以便宿主机通过映射端口访问
- 环境变量覆盖格式: `WECLAW_OPENCLAW_API_KEY=xxx`
