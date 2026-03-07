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

## 核心开发和避坑指南（基于真实踩坑总结）

### 1. OpenClaw Docker 配置与挂载
- **配置文件名**: 配置文件叫 `openclaw.json` (不是 config.json5)，位于容器的 `/home/node/.openclaw/` 目录下。
- **配置注入方式**: 不能用 Docker `Cmd` 执行 `sh -c` 写入，因为 `/home/node/.openclaw` 默认不存在会报错。正确做法是在宿主机新建专属目录和 `openclaw.json`，然后通过 Docker 的 Bind Mount 挂载进容器。
- **Gateway 暴露设置**: Gateway 默认只绑定 `127.0.0.1`，要让宿主机能够访问，必须满足两个条件：
  1. 启动命令加上 `--bind lan`
  2. 配置文件里设置 `gateway.controlUi.dangerouslyAllowHostHeaderOriginFallback=true`

### 2. 模型调用代理映射
- OpenClaw 设置 `Shell env = off`，会**刻意忽略** `OPENAI_BASE_URL` 和 `ANTHROPIC_BASE_URL` 的环境变量。
- **正确做法**: 在生成的 `openclaw.json` 中定义自己的自定义 Provider (`models.providers`)，设置其 `baseUrl` 为你自己的代理地址，并指定模型（如 `claude-sonnet-4-6`），强制系统走你的第三方代理。

### 3. OpenClaw Gateway 通讯机制
- 官方启动显示的 Gateway 其实走的是 **WebSocket** 协议（运行在 18789 端口）。
- **它并没有 HTTP REST API** (没有 `/v1/responses` 或 `/v1/chat/completions`)。
- **如何通信**: 我们目前使用的是稳定的 `docker exec` 调用方式：
  ```bash
  docker exec <container> openclaw agent --agent main --json -m "你的消息"
  ```
  这样能获得完整的 JSON 输出（响应文本在 `result.payloads[0].text` 中）。Agent 的初次运行有 Bootstrap 流程（读取 BOOTSTRAP.md 等），后续能正常获得输出文字。遇到上游请求拥塞可能会发生空返回的长超时，可以通过直接进入容器执行同样命令排查大模型提供商的抖动。

### 4. 健壮性与稳定性
- **字符串切片越界防范**: 对 OpenID 等字符串截取前务必检查长度（如 `len(id) > 12`），直接使用 `[:12]` 容易引发 Go runtime slice bounds out of range panic，导致进程崩溃和状态假死。
- **幽灵容器容错自愈**: 当容器从 Docker 引擎中丢失（例如被手动回收处理了），Go 代码中处理闲置容器的 `StopContainer` 若收到 `No such container` 错误应予忽略不再重试。路由层在下次唤醒（Wakeup）该用户的失效容器时若出现丢失报错，应当直接跳入自愈逻辑（自动销毁陈旧元数据并触发重建其专属容器），保证对用户的 0 故障恢复。

## 项目结构

```
weclaw/
├── cmd/weclaw/main.go           # 程序入口
├── internal/
│   ├── account/                 # 管理员账号储存管理模块
│   ├── api/                     # 控制面板测试及模型仿真 API（附带 JWT Auth 中间件）
│   ├── config/config.go         # 配置文件映射与读取
│   ├── wechat/                  # 微信公众号消息事件和客服消息对接
│   ├── container/               # Docker Manager (创建模型配置文件与挂载管理)
│   ├── openclaw/                # 对接 OpenClaw 指令交互的核心 Client
│   ├── user/                    # To-C 用户会话管理 (包含状态及消息限额)
│   ├── router/router.go         # 全局核心消息处理及故障自愈路由
│   └── store/store.go           # 包装全局 GORM
├── web/index.html               # 带有登录和日志的 Web 控制台界面
├── configs/config.yaml          # 默认及参考启动配置
├── scripts/test.sh              # 外部环境测试脚本
└── experience/                  # 容器及网络问题的踩坑记录库
```

## 开发命令

```bash
# 运行代码
go run cmd/weclaw/main.go

# 构建可执行文件
go build -o bin/weclaw cmd/weclaw/main.go
```

## 注意事项

- 微信被动回复有 **5 秒超时限制**，必须立即给微信回复“收到请稍后等短被动回复”，同时开启 goroutine 以异步方式处理 AI 响应后续走客服接口下发结果。
- 订阅号需要认证才能使用 `客服消息下发接口`。
- 不活跃容器会自动休眠以节省云服务器资源（只关闭进程，唤醒时再 Start）。
- 任何调试、安全监控以及开发测试均可配合内置的 JWT Web 控制面板和 HTTP Token API。
