# WeClaw - 微信公众号 OpenClaw 网关

## 项目概述

WeClaw 是一个 Golang 后端服务，通过微信公众号（订阅号）让普通用户使用 OpenClaw AI 助手。
用户关注订阅号后，系统自动创建一个 Docker 容器运行 OpenClaw，用户通过微信消息与自己的 OpenClaw 实例交互。

系统支持**共享知识库**（全局 Docker Volume）、**Skills 商店**和 **MCP Server 商店**，用户可通过 Web 控制面板自定义安装 Skills 和 MCP Server 到自己的容器中。

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

### 5. 安全性与权限放通
- **大模型系统权限 (Tools Profile)**：默认情况 OpenClaw 处于安全模式限制模型使用进阶工具（无法直接对宿主机/容器内部执行危险操作）。在本项目中如果需要深度授权（如执行脚本），我们已在项目全局配置了注入通道，通过在 `configs/config.yaml` 或者环境变量中配置 `openclaw.tools_profile: "full"`，创建的容器便会自动将对应的安全层级设定以原生 JSON 配置的方式内嵌至容器，全自动化解除限制。

### 6. Skills 与 MCP Server 配置注入
- **Skills 配置**: OpenClaw 的 Skills 通过 `openclaw.json` 中的 `skills.entries` 字段配置。每个 skill 条目有 `enabled`（布尔）和可选的 `config`（对象）。可通过 `skills.load.extraDirs` 指定额外的 skill 目录。
- **MCP Server 配置**: MCP（Model Context Protocol）服务器通过 `openclaw.json` 中的 `provider.mcpServers` 字段配置。每个 MCP 条目需要 `command`（可执行文件）、`args`（参数数组）和可选的 `env`（环境变量）。
- **动态配置更新**: 用户通过 Web UI 安装/卸载 Skill 或 MCP 后，调用 `POST /api/user/apply` 触发 `RegenerateConfig()`，该方法会重写宿主机上的 `openclaw.json` 并重启容器使配置生效。
- **数据库持久化**: 用户的 Skill/MCP 选择存储在 SQLite 的 `user_skills` 和 `user_mcps` 表中（通过 `catalog` 包管理），创建容器时会查询这些表构建 `OpenClawExtras` 注入配置。

### 7. 共享知识库（宿主机目录 Bind Mount）
- 系统启动时自动创建宿主机共享知识库目录（默认 `./data/shared-knowledge`）。
- 所有用户容器以**只读**模式 Bind Mount 该目录到 `/home/node/shared-knowledge`。
- 管理员直接向宿主机目录放置文件即可，所有 OpenClaw 实例均可读取。
- 创建容器时自动写入 `AGENTS.md` 到容器 workspace（`~/.openclaw/`），告知 agent 共享知识库路径，使 agent 自动纳入系统提示词。
- 配置项在 `configs/config.yaml` 的 `knowledge_base` 段（`host_dir` + `container_mount`）。
- Web UI 左侧栏展示知识库文件树，点击文件可直接查看内容。

### 8. OpenClaw 系统提示词注入机制
- OpenClaw **不支持在 `openclaw.json` 中直接设置 system prompt**，而是通过 workspace 目录下的约定文件自动注入：
  - `SOUL.md` — 核心身份和行为特征（仅主 agent）
  - `AGENTS.md` — 操作指令和持久化记忆（所有 agent）
  - `TOOLS.md` — 工具使用说明
  - `USER.md` — 用户信息
  - `MEMORY.md` — 长期记忆
  - `BOOTSTRAP.md` — 首次启动配置（运行后自删除）
- 这些文件放在 `~/.openclaw/` 目录下（即我们 bind mount 的宿主机目录中），会被自动拼接进系统提示词。
- 每个文件上限约 20K 字符，总上限 150K 字符（可通过 `agents.defaults.bootstrapMaxChars` 调整）。

## 项目结构

```
weclaw/
├── cmd/weclaw/main.go           # 程序入口
├── internal/
│   ├── account/                 # 管理员账号储存管理模块
│   ├── api/                     # REST API 端点
│   │   ├── auth.go              # JWT 认证（登录/注册 + AuthMiddleware）
│   │   ├── test_api.go          # 容器测试与调试 API
│   │   ├── openai_api.go        # OpenAI 兼容 API（/v1/chat/completions）
│   │   └── store_api.go         # Skills/MCP 商店 + 用户配置 + Apply API
│   ├── catalog/                 # Skills/MCP 商店与用户选择管理
│   │   ├── model.go             # SkillCatalog, UserSkill, MCPCatalog, UserMCP 模型
│   │   └── service.go           # CRUD + BuildOpenClawExtras()
│   ├── config/config.go         # 配置文件映射与读取（含 KnowledgeBaseConfig）
│   ├── wechat/                  # 微信公众号消息事件和客服消息对接
│   ├── container/               # Docker Manager
│   │   ├── manager.go           # 容器创建/挂载/Skills+MCP注入/RegenerateConfig/共享Volume
│   │   └── pool.go              # 端口池管理
│   ├── openclaw/                # 对接 OpenClaw 指令交互的核心 Client
│   ├── user/                    # To-C 用户会话管理 (包含状态及消息限额)
│   ├── router/router.go         # 全局核心消息处理及故障自愈路由
│   └── store/store.go           # 包装全局 GORM（AutoMigrate 6 张表）
├── web/index.html               # ChatGPT 风格三栏 Web 控制面板
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

- 微信被动回复有 **5 秒超时限制**，必须立即给微信回复”收到请稍后等短被动回复”，同时开启 goroutine 以异步方式处理 AI 响应后续走客服接口下发结果。
- 订阅号需要认证才能使用 `客服消息下发接口`。
- 不活跃容器会自动休眠以节省云服务器资源（只关闭进程，唤醒时再 Start）。
- 任何调试、安全监控以及开发测试均可配合内置的 JWT Web 控制面板和 HTTP Token API。

## API 端点汇总

### 认证
- `POST /api/auth/register` — 注册管理员
- `POST /api/auth/login` — 登录获取 JWT

### 测试/调试（JWT 保护）
- `GET /api/test/docker` — Docker 连通性测试
- `POST /api/test/register` — 模拟用户注册 + 创建容器
- `POST /api/test/send` — 发送消息到用户容器
- `GET /api/test/user/:openid` — 查看用户状态
- `GET /api/test/user/:openid/messages` — 获取用户消息历史
- `GET /api/test/users` — 列出所有用户
- `DELETE /api/test/user/:openid` — 删除用户 + 容器

### Skills/MCP 商店（JWT 保护）
- `GET /api/store/skills` — 列出 Skill 商店
- `POST /api/store/skills` — 添加 Skill 到商店
- `DELETE /api/store/skills/:name` — 移除商店 Skill
- `GET /api/store/mcps` — 列出 MCP 商店
- `POST /api/store/mcps` — 添加 MCP 到商店
- `DELETE /api/store/mcps/:name` — 移除商店 MCP
- `GET /api/store/knowledge` — 列出共享知识库文件树
- `GET /api/store/knowledge/read?path=xxx` — 读取知识库文件内容

### 用户扩展配置（JWT 保护）
- `GET /api/user/skills?openid=xxx` — 用户已安装 Skill 列表
- `POST /api/user/skills` — 为用户安装 Skill
- `DELETE /api/user/skills/:name?openid=xxx` — 卸载 Skill
- `GET /api/user/mcps?openid=xxx` — 用户已安装 MCP 列表
- `POST /api/user/mcps` — 为用户安装 MCP Server
- `PUT /api/user/mcps/:name` — 更新 MCP 配置
- `DELETE /api/user/mcps/:name?openid=xxx` — 卸载 MCP
- `POST /api/user/apply` — **应用变更**（重写 openclaw.json + 重启容器）

### OpenAI 兼容
- `POST /v1/chat/completions` — OpenAI API 兼容端点（Bearer token 为 openid）
