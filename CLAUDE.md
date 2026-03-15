# WeClaw - Web 端 OpenClaw 网关

## 项目概述

WeClaw 是一个 Golang 后端服务，提供 Web 端多容器管理平台，让用户通过浏览器使用 OpenClaw AI 助手。
每个注册用户（Account）可以创建**多个** Docker 容器，每个容器运行独立的 OpenClaw 实例。用户通过 Web Dashboard 管理容器、发送消息、安装 Skills 和 MCP Server。

系统支持**共享知识库**（宿主机 Bind Mount）、**Skills 商店**和 **MCP Server 商店**，用户可通过 Web 控制面板自定义安装到自己的容器中。

## 架构模型

- **Account** (1) → (N) **Container**：每个用户可拥有多个容器
- **Container** (1) → (N) **UserSkill / UserMCP / MessageLog**：Skills/MCP/消息日志绑定到容器
- **ChatRoom** (N) ←→ (N) **Account+Container**：群聊房间，多用户多 Agent 参与
- 所有 API 从 JWT 的 `account_id` 出发，URL 中的 `:id` 为 Container 数据库 ID
- 权限隔离：后端从 JWT 取 accountID，所有查询 WHERE account_id = ?

## 技术栈

- **语言**: Go 1.25+
- **Web 框架**: Gin
- **WebSocket**: gorilla/websocket
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
- Gateway 运行在容器内 18789 端口，映射到宿主机动态端口（存储在 `Container.ContainerPort`）。
- **流式通信架构**（主路径）：
  ```
  Browser ←— WebSocket —→ WeClaw Go Backend ←— SSE —→ OpenClaw Gateway /v1/responses
  ```
  - 前端通过 WebSocket (`GET /ws/containers/:id?token=<JWT>`) 连接后端
  - 后端收到 `send_message` 后，向 Gateway 的 `/v1/responses` 端点发起 SSE 流式请求
  - SSE 事件（`response.output_text.delta` / `response.output_text.done` / `response.completed`）逐个转发为 WebSocket `stream_delta` / `stream_done` 消息
  - 优势：无超时限制（WebSocket 无 HTTP 超时）；用户看到逐 token 实时输出；支持 slash command
- **同步通信**（保留兼容）：
  ```
  POST http://127.0.0.1:<port>/v1/chat/completions
  Authorization: Bearer <gatewayToken>
  Body: {"model":"openclaw:main","messages":[{"role":"user","content":"消息"}]}
  ```
  响应为标准 OpenAI Chat Completion 格式，取 `choices[0].message.content`。仍用于 `POST /api/containers/:id/send` 和 `/v1/chat/completions` 端点。
- **启用条件**: `openclaw.json` 中需配置 `gateway.http.endpoints.chatCompletions.enabled: true` + `gateway.http.endpoints.responses.enabled: true` 以及 `gateway.auth.mode: "token"` + `gateway.auth.token`（由 `manager.go` 自动生成写入）。
- **降级策略**: 如果 Gateway 的 `/v1/responses` 返回 404（旧容器未启用 responses endpoint），`StreamMessage()` 自动回退到同步 `SendMessage()`，发一个 `text_done` 事件。
- **历史**: 早期使用 `docker exec openclaw agent --json -m "..."` 方式，但存在 Go context timeout 导致进程被 SIGKILL、以及首次 bootstrap 空返回需 hack 重试的问题，已弃用。后续改为 HTTP POST 同步调用 `/v1/chat/completions`，但存在超时问题和无流式反馈，现已升级为 WebSocket + SSE 流式架构。

### 4. 健壮性与稳定性
- **字符串切片越界防范**: 对 ContainerID 等字符串截取前务必检查长度（如 `len(id) > 12`），直接使用 `[:12]` 容易引发 Go runtime slice bounds out of range panic。
- **幽灵容器容错**: 当容器从 Docker 引擎中丢失，`StopContainer` 若收到 `No such container` 错误应予忽略不再重试。

### 5. 安全性与权限放通
- **大模型系统权限 (Tools Profile)**：通过在 `configs/config.yaml` 或者环境变量中配置 `openclaw.tools_profile: "full"`，创建的容器便会自动将对应的安全层级设定以原生 JSON 配置的方式内嵌至容器。

### 6. Skills 与 MCP Server 配置注入
- **Skills 配置**: OpenClaw 的 Skills 通过 `openclaw.json` 中的 `skills.entries` 字段配置。
- **MCP Server 配置**: 通过 `openclaw.json` 中的 `provider.mcpServers` 字段配置。
- **动态配置更新**: 用户通过 Web UI 安装/卸载后，调用 `POST /api/containers/:id/apply` 触发 `RegenerateConfig()`。
- **数据库持久化**: 用户的 Skill/MCP 选择存储在 SQLite 的 `user_skills` 和 `user_mcps` 表中（外键为 `container_id`）。

### 7. Web 搜索配置（可选）
- **配置位置**: `configs/config.yaml` 的 `openclaw.web_search` 段。
- **配置注入**: `prepareOpenClawHostDir()` 在构建 `openclaw.json` 时，若 `WebSearch` 非空且 `Enabled` 为 true，会将配置注入到 `tools.web.search` 节中（对应 OpenClaw 的 `tools.web.search` 结构）。
- **生效方式**: 新容器自动生效；已有容器需通过 `POST /api/containers/:id/apply` 触发 `RegenerateConfig()` 重写 `openclaw.json` 并重启容器。
- **配置结构**: `WebSearchConfig` 包含 `enabled`（bool）、`api_key`、`max_results`、`timeout_seconds`、`cache_ttl_minutes`，`enabled` 为 false 或未配置时不注入。

### 8. 共享知识库（宿主机目录 Bind Mount）
- 系统启动时自动创建宿主机共享知识库目录（默认 `./data/shared-knowledge`）。
- 所有用户容器以**读写**模式 Bind Mount 该目录到 `/home/node/shared-knowledge`。OpenClaw 可以读取并写入共享知识库。
- 创建容器时自动写入 `USER.md` 到容器 workspace 目录（`~/.openclaw/workspace/USER.md`），告知 agent 共享知识库路径。
- **注意**：OpenClaw 约定文件（`AGENTS.md`、`USER.md` 等）的读取路径是 `~/.openclaw/workspace/`，不是 `~/.openclaw/` 根目录。`AGENTS.md` 由 OpenClaw 在 bootstrap 时自动生成，不要覆盖。`USER.md` 每次会话都会被 agent 主动读取，且不会被 OpenClaw 自动覆盖。
- 配置项在 `configs/config.yaml` 的 `knowledge_base` 段。
- Web 侧边栏展示共享知识库文件树，支持点击查看文本文件内容。

### 9. OpenClaw 系统提示词注入机制
- OpenClaw 通过 **workspace 目录**（`~/.openclaw/workspace/`）下的约定文件自动注入系统提示词，**不是** `~/.openclaw/` 根目录：
  - `SOUL.md` — 核心身份和行为特征（仅主 agent）
  - `AGENTS.md` — 操作指令和持久化记忆（所有 agent，由 OpenClaw bootstrap 自动生成，不要覆盖）
  - `TOOLS.md` — 工具使用说明
  - `USER.md` — 用户信息（WeClaw 在此写入共享知识库路径等环境信息）
  - `MEMORY.md` — 长期记忆
  - `BOOTSTRAP.md` — 首次启动配置（运行后自删除）

### 10. 多会话与 Switch Bar（聊天页标签栏）
- **Switch Bar** 位于聊天页 `chat-header` 下方，统一管理私聊 Session 和群聊 Room 的切换。
- **多 Session 支持**: 每个容器连接下可创建多个独立会话标签（Chat 1, Chat 2...），每个 session 拥有独立的 `lastResponseID`，通过 WebSocket `send_message` 中的 `session_tag` 字段区分。
- **后端 `lastResponseIDs`**: `wsConn.lastResponseIDs` 是 `map[string]string`（key 为 `session_tag`），支持同一个 WebSocket 连接上的多会话隔离。前端发送 `session_tag: "session-<id>"`，后端据此存取不同的 `lastResponseID`。
- **Room 标签**: 用户已加入的群聊房间也作为标签出现在 Switch Bar 中，点击即切换到群聊视图（加载房间消息 + 打开房间 WebSocket + 显示成员栏）。
- **统一消息区**: `messagesArea` 和 `messageInput` 在 Session 和 Room 之间共享复用，切换 Session 时保存/恢复 HTML 快照，切换 Room 时从 API 重新加载。
- **Dashboard 无 Group Chats 区域**: 群聊入口已全部移到 Switch Bar 的 `+ Room` / `Join` 按钮中。

### 11. 群聊邀请用户
- **API**: `POST /api/rooms/:roomId/invite` body: `{ "username": "xxx" }`
- **流程**: 验证调用者是房间成员 → 通过 `FindByUsername` 查找目标用户 → 通过 `GetFirstActiveByAccount` 获取其第一个容器 → `JoinRoom` 添加成员 → 广播 `member_list` 更新。
- **前端入口**: 房间成员栏末尾的 "+ Invite" 按钮，弹出模态框输入用户名。

## 项目结构

```
weclaw/
├── cmd/weclaw/main.go           # 程序入口
├── internal/
│   ├── account/                 # 用户账号模型与仓库
│   │   ├── model.go             # Account GORM 模型
│   │   ├── repository.go        # Repository 接口
│   │   └── sqlite_repository.go # SQLite 实现
│   ├── api/                     # REST API + WebSocket 端点
│   │   ├── auth.go              # JWT 认证（登录/注册 + AuthMiddleware + parseJWT）
│   │   ├── openai_api.go        # OpenAI 兼容 API（JWT + X-Container-ID）
│   │   ├── room_api.go          # 群聊房间 REST API（CRUD + 邀请 + 成员管理 + NewSession）
│   │   ├── store_api.go         # Container CRUD + Skills/MCP 配置 + Store + Knowledge + 路由注册
│   │   ├── ws.go                # 容器 WebSocket（connHub + 多 session 流式消息转发 + Gateway 状态推送）
│   │   └── ws_room.go           # 群聊 WebSocket（@mention 调度 + 多 Agent 流式响应）
│   ├── catalog/                 # Skills/MCP 商店与容器选择管理
│   │   ├── model.go             # SkillCatalog, UserSkill, MCPCatalog, UserMCP 模型 (ContainerID 外键)
│   │   └── service.go           # CRUD + BuildOpenClawExtras(containerID)
│   ├── config/config.go         # 配置文件映射与读取
│   ├── container/               # Docker 管理 + 容器业务层
│   │   ├── model.go             # Container + MessageLog GORM 模型
│   │   ├── manager.go           # Docker 操作层（创建/挂载/配置注入/共享知识库）
│   │   ├── service.go           # 业务 CRUD（ListByAccount/Create/Delete/ApplyChanges/GetFirstActiveByAccount）
│   │   └── pool.go              # 端口池管理
│   ├── groupchat/               # 群聊房间管理
│   │   ├── model.go             # ChatRoom, ChatRoomMember, ChatRoomMessage GORM 模型
│   │   └── service.go           # 房间 CRUD + 成员管理 + 消息持久化
│   ├── openclaw/                # 对接 OpenClaw 指令交互
│   │   ├── client.go            # Gateway HTTP API 方式发送消息（同步 + streamHTTPClient）
│   │   ├── stream.go            # SSE 流式客户端（StreamMessage → /v1/responses，降级回退）
│   │   └── formatter.go         # 文本格式化工具
│   └── store/store.go           # 包装全局 GORM（AutoMigrate 所有表）
├── web/index.html               # Container Dashboard Web UI
├── configs/config.yaml          # 默认启动配置
├── scripts/                     # 外部脚本
└── experience/                  # 踩坑记录库
```

## 开发命令

```bash
# 运行代码（非 root 用户需 sudo，详见下方权限说明）
sudo go run cmd/weclaw/main.go

# 构建可执行文件
go build -o bin/weclaw cmd/weclaw/main.go
sudo ./bin/weclaw
```

> **权限要求：** OpenClaw 容器以 `node` 用户（UID 1000）运行，WeClaw 创建 Bind Mount 目录后需要 `chown` 为 1000:1000。如果当前用户不是 root 且 UID 不是 1000，必须使用 `sudo` 运行，否则容器内会报 `EACCES: permission denied`。

## 注意事项

- 不活跃容器会自动休眠以节省云服务器资源（只关闭进程，唤醒时再 Start）。
- 所有 API 调用需要 JWT 认证，后端从 JWT 取 accountID 进行权限隔离。
- 创建容器时 Docker container name 格式为 `weclaw-openclaw-acct{accountID}-ctr{containerID}`。

## API 端点汇总

### 认证
- `POST /api/auth/register` — 注册账号
- `POST /api/auth/login` — 登录获取 JWT

### 容器管理（JWT 保护）
- `GET /api/containers` — 列出当前账号的所有容器
- `POST /api/containers` — 创建新容器（body: `{display_name}`）
- `GET /api/containers/:id` — 容器详情（含 Docker 运行状态）
- `DELETE /api/containers/:id` — 删除容器

### 容器交互（JWT 保护）
- `POST /api/containers/:id/send` — 发消息到容器（同步 HTTP，保留兼容）
- `GET /api/containers/:id/messages` — 消息历史

### WebSocket 流式通信
- `GET /ws/containers/:id?token=<JWT>` — WebSocket 连接（JWT 走 query param）
  - Client → Server: `send_message` (含 `session_tag` 字段用于多会话隔离) / `ping_gateway` / `pong`
  - Server → Client: `connected` / `gateway_status` / `stream_start` / `stream_delta` / `stream_done` / `stream_error` / `ping` / `error`
- `GET /ws/rooms/:roomId?token=<JWT>` — 群聊 WebSocket 连接
  - Client → Server: `send_message` / `pong`
  - Server → Client: `connected` / `member_list` / `room_message` / `room_stream_start` / `room_stream_delta` / `room_stream_done` / `room_stream_error` / `ping` / `error`

### 容器扩展配置（JWT 保护）
- `GET /api/containers/:id/skills` — 容器已装 Skill
- `POST /api/containers/:id/skills` — 安装 Skill
- `DELETE /api/containers/:id/skills/:name` — 卸载 Skill
- `GET /api/containers/:id/mcps` — 容器已装 MCP
- `POST /api/containers/:id/mcps` — 安装 MCP
- `PUT /api/containers/:id/mcps/:name` — 更新 MCP 配置
- `DELETE /api/containers/:id/mcps/:name` — 卸载 MCP
- `POST /api/containers/:id/apply` — 应用变更（重写 openclaw.json + 重启）

### Skills/MCP 商店（JWT 保护）
- `GET /api/store/skills` — 列出 Skill 商店
- `POST /api/store/skills` — 添加 Skill 到商店
- `DELETE /api/store/skills/:name` — 移除商店 Skill
- `GET /api/store/mcps` — 列出 MCP 商店
- `POST /api/store/mcps` — 添加 MCP 到商店
- `DELETE /api/store/mcps/:name` — 移除商店 MCP
- `GET /api/store/knowledge` — 列出共享知识库文件树
- `GET /api/store/knowledge/read?path=xxx` — 读取知识库文件内容

### 群聊房间（JWT 保护）
- `GET /api/rooms` — 列出当前用户已加入的房间
- `POST /api/rooms` — 创建新房间（body: `{name, container_id}`）
- `DELETE /api/rooms/:roomId` — 删除房间（仅创建者）
- `POST /api/rooms/:roomId/join` — 加入房间（body: `{container_id}`）
- `POST /api/rooms/:roomId/invite` — 邀请用户（body: `{username}`）
- `POST /api/rooms/:roomId/leave` — 离开房间
- `GET /api/rooms/:roomId/members` — 获取房间成员列表
- `GET /api/rooms/:roomId/messages` — 获取房间消息历史

### OpenAI 兼容
- `POST /v1/chat/completions` — OpenAI API 兼容端点（JWT + X-Container-ID header）
