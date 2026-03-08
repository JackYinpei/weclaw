# Skills/MCP 商店与共享知识库集成经验总结

> 2026-03-08，基于 WeClaw Skills/MCP Store 功能开发过程

---

## 1. OpenClaw Skills 配置格式

### 核心发现

OpenClaw 的 Skills 不是简单的 prompt 模板，而是一套基于 `SKILL.md` 文件的 agent 扩展机制。在 `openclaw.json` 中通过两个维度配置：

1. **`skills.entries`** — 控制每个 skill 的启用/禁用和参数
2. **`skills.load.extraDirs`** — 指定额外的 skill 目录

```json
{
  "skills": {
    "load": {
      "extraDirs": ["/home/node/shared-knowledge/skills"],
      "watch": true
    },
    "entries": {
      "web-search": { "enabled": true },
      "exec": { "enabled": false },
      "custom-skill": {
        "enabled": true,
        "config": { "endpoint": "https://example.com" },
        "env": { "API_KEY": "xxx" }
      }
    }
  }
}
```

### 关键点
- `entries` 的 key 是 skill 名称（对应 skill 目录名或 `metadata.openclaw.skillKey`）
- `config` 字段是自定义键值对，传递给 skill 内部使用
- `env` 字段注入环境变量到 agent 进程
- `watch: true` 会自动检测 skill 文件变更并在下一轮 agent 对话中生效

## 2. OpenClaw MCP Server 配置格式

### 核心发现

MCP Server 配置放在 `openclaw.json` 的 `provider.mcpServers` 下（**不是** 顶层的 `mcpServers`）：

```json
{
  "provider": {
    "mcpServers": {
      "tavily": {
        "command": "npx",
        "args": ["-y", "tavily-mcp@latest"],
        "env": { "TAVILY_API_KEY": "xxx" }
      },
      "filesystem": {
        "command": "node",
        "args": ["/path/to/mcp-server/dist/index.js"]
      }
    }
  }
}
```

### 注意事项
- `command` + `args` 模式是 `stdio` 传输（默认），适合容器内本地运行
- MCP Server 需要在容器内可执行（npx 需要 Node.js 环境，容器内已有）
- 每次修改 MCP 配置后需要**重启容器**才能生效（不像 skills 有 watch 机制）

## 3. 动态配置更新（RegenerateConfig）的设计取舍

### 问题

用户在 Web UI 安装/卸载 Skill 或 MCP 后，如何让配置在容器中生效？

### 考虑过的方案

| 方案 | 优点 | 缺点 |
|------|------|------|
| docker exec 写入配置 | 不需重启 | 容器内目录权限问题，与 bind mount 冲突 |
| Docker API CopyToContainer | 直接写入容器文件系统 | 技术复杂度高，tar 打包 |
| **重写宿主机 bind mount 目录 + 重启容器** | 简单可靠，与现有架构一致 | 需要短暂停机（几秒） |

### 最终选择

选择方案 3（重写 + 重启），原因：
1. 与现有的 `prepareOpenClawHostDir` bind mount 机制完全一致
2. 容器重启只需 3-5 秒，用户感知很短
3. 确保所有配置（gateway + model + skills + MCP）都在同一个 `openclaw.json` 中原子性更新

### 实现细节

`RegenerateConfig()` 方法流程：
1. 从 DB 查询用户的 `UserSkill` 和 `UserMCP` 记录
2. 调用 `BuildOpenClawExtras()` 构建 `OpenClawExtras` 结构
3. 调用 `prepareOpenClawHostDir()` 重写宿主机上的 `openclaw.json`
4. Docker SDK `ContainerStop()` + `ContainerStart()` 重启容器

## 4. 共享知识库 Volume 设计

### Docker Named Volume vs Bind Mount

选择 **Docker Named Volume** 而非 Bind Mount 的原因：
- Named Volume 生命周期由 Docker 管理，不依赖宿主机特定路径
- 多容器共享更简洁：直接用 volume 名称引用
- 跨平台一致性更好（macOS Docker Desktop 对 bind mount 有性能问题）

### 挂载方式

```go
binds = append(binds, "weclaw-shared-knowledge:/home/node/shared-knowledge:ro")
```

`:ro`（只读）是关键——防止任何用户容器意外修改共享内容。管理员通过外部方式（`docker cp` 或临时容器）写入内容。

## 5. resolveUser 的 JSON body 消费陷阱

### 问题

在 `store_api.go` 中，`resolveUser()` 方法尝试从 query param 和 JSON body 两个来源获取 `openid`。但 Gin 的 `ShouldBindJSON()` 会消费 `c.Request.Body`，导致后续的 handler 无法再次读取 body。

### 解决方案

对于 GET/DELETE 请求使用 query param（`?openid=xxx`），对于 POST/PUT 请求在各自的 handler 中独立解析 body（包含 openid 字段）。`resolveUser()` 仅用于 GET 请求场景。

## 6. GORM replace_all 和 Go 字符串匹配的微妙差异

### 踩坑

使用编辑器的 `replace_all` 替换 `CreateContainer` 调用时，因为两处调用的**缩进不同**（一处用 tab、一处用空格，或有额外的换行），导致只替换了一处。

### 经验

- 修改函数签名后，必须 `go build` 验证，编译器会精确报错所有未更新的调用点
- 不要依赖文本搜索替换，编译器才是最可靠的检查器

## 7. 商店数据模型的 Composite Unique Index

### 设计

`UserSkill` 和 `UserMCP` 使用 `(UserID, SkillName)` / `(UserID, MCPName)` 的复合唯一索引：

```go
UserID    uint   `gorm:"uniqueIndex:idx_user_skill;not null"`
SkillName string `gorm:"uniqueIndex:idx_user_skill;size:128;not null"`
```

这确保同一用户不会重复安装同一个 Skill/MCP，GORM 的 `uniqueIndex` tag 名相同即表示属于同一个复合索引。

## 8. Docker Named Volume vs 宿主机 Bind Mount 的最终选择

### 第一版：Named Volume

最初共享知识库使用 Docker Named Volume（`weclaw-shared-knowledge`），通过 Docker SDK 的 `VolumeCreate` 创建。

### 问题

后端（Go 进程）需要直接读取共享知识库文件（用于 Web UI 的文件树展示和文件查看），但 Docker Named Volume 的宿主机路径不可移植（Linux 在 `/var/lib/docker/volumes/`，macOS Docker Desktop 在虚拟机里），Go 进程无法直接访问。

### 最终方案：宿主机目录 Bind Mount

改为使用宿主机目录（`./data/shared-knowledge`）+ bind mount `:ro`：
- Go 后端用 `os.ReadDir` / `os.ReadFile` 直接读取文件
- 管理员直接 `cp` 文件到目录，无需 `docker cp` 或临时容器
- 与现有 `.openclaw` 配置目录的 bind mount 模式完全一致

### 经验

选择存储方案时，优先考虑**谁需要读写**。如果宿主机进程需要访问数据，bind mount 比 named volume 更实用。

## 9. OpenClaw 系统提示词注入：workspace 约定文件

### 关键发现

OpenClaw **不支持在 JSON 配置中直接写 system prompt**。它通过读取 workspace 目录下的约定文件自动组装系统提示词：

| 文件 | 作用 | 注入范围 |
|------|------|----------|
| `SOUL.md` | 核心身份/人格 | 仅主 agent |
| `AGENTS.md` | 操作指令/记忆 | 所有 agent |
| `TOOLS.md` | 工具使用说明 | 所有 agent |
| `USER.md` | 用户信息 | 仅主 agent |
| `MEMORY.md` | 长期记忆 | 仅主 agent |
| `BOOTSTRAP.md` | 首次启动（运行后自删） | 仅主 agent |

### 在 WeClaw 中的应用

我们在 `prepareOpenClawHostDir()` 中写入 `AGENTS.md` 到 `~/.openclaw/` 目录（bind mount 到容器），告诉 agent 共享知识库的挂载路径。这样所有 agent（包括子 agent）都能知道去哪里读取共享文档。

```go
agentsMD := fmt.Sprintf("# Shared Knowledge Base\n\n"+
    "A shared knowledge base is mounted at `%s` (read-only).\n"+
    "When users ask questions, check this directory for relevant documents.\n",
    m.kbCfg.ContainerMount)
os.WriteFile(filepath.Join(hostDir, "AGENTS.md"), []byte(agentsMD), 0644)
```

## 10. 前端自动重连：localStorage 持久化用户状态

### 问题

刷新页面后用户需要重新输入 OpenID 并点击连接，体验很差。每个容器是与用户固定绑定的，不应该每次都要手动连接。

### 解决方案

用 `localStorage` 保存当前连接的 OpenID：
- 连接成功时：`localStorage.setItem('weclaw_openid', openid)`
- 页面加载时：检测到已保存的 openid 则自动调 `connectUser()`
- 切换用户时：`localStorage.removeItem('weclaw_openid')`

同时增加消息历史 API（`GET /api/test/user/:openid/messages`），连接时加载最近 50 条对话记录，而不是显示空白聊天框。

## 11. 知识库文件读取的路径穿越防护

在 `ReadKnowledgeFile` API 中，用户通过 `?path=xxx` 传入文件路径。必须防止路径穿越攻击：

```go
cleanPath := filepath.Clean(reqPath)
if strings.Contains(cleanPath, "..") { /* 拒绝 */ }

absKB, _ := filepath.Abs(hostDir)
absFile, _ := filepath.Abs(fullPath)
if !strings.HasPrefix(absFile, absKB) { /* 拒绝 */ }
```

双重检查：先过滤 `..`，再验证绝对路径前缀。另外限制文件大小（1MB）防止读取大文件导致内存问题。
