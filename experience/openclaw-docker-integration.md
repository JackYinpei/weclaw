# OpenClaw Docker 容器集成经验总结

> 截至 2026-03-06，基于 `ghcr.io/openclaw/openclaw:latest` (v2026.3.2)
> 2026-03-09 更新：架构已重构为 Account 1:N Container 模型，微信相关代码已移除。以下 Docker/OpenClaw 技术细节仍然有效。

---

## 1. 镜像基本信息

- 镜像: `ghcr.io/openclaw/openclaw:latest`
- 运行用户: `node` (uid 1000)，HOME 为 `/home/node`
- 入口: `docker-entrypoint.sh`，默认 CMD 是 `node dist/index.js`
- CLI 工具: `/usr/local/bin/openclaw`
- Gateway 默认端口: **18789** (容器内)

## 2. 配置文件（关键踩坑点）

### 配置文件名是 `openclaw.json`，不是 `config.json5`

```bash
# 正确
~/.openclaw/openclaw.json

# 错误（之前踩的坑）
~/.openclaw/config.json5
```

可通过容器内命令验证：
```bash
docker exec <container> openclaw config file
# 输出: ~/.openclaw/openclaw.json
```

### 配置读取验证

```bash
docker exec <container> openclaw config get gateway.controlUi.dangerouslyAllowHostHeaderOriginFallback
# 应输出: true
```

## 3. Gateway 绑定到 LAN（宿主机访问）

### 问题

Gateway 默认只监听 `127.0.0.1`（容器内 loopback），宿主机通过 Docker 端口映射无法访问，会得到 `Empty reply from server` 或 `Connection refused`。

### 解决方案

**同时满足两个条件：**

1. 启动时传 `--bind lan`（或环境变量 `OPENCLAW_GATEWAY_BIND=lan`）
2. 配置文件中设置 `gateway.controlUi.dangerouslyAllowHostHeaderOriginFallback=true`

仅设置其中一个都不够 —— 缺少 bind=lan 宿主机访问不到，缺少 dangerouslyAllow 配置则 Gateway 启动报错并循环重启。

## 4. 配置注入方式：宿主机目录 Bind Mount

### 为什么不能在容器内写

容器 Cmd 中用 `sh -c "mkdir -p ... && echo ... > ..."` 写配置会失败：
```
cannot create /home/node/.openclaw/config.json5: Directory nonexistent
```
因为镜像内 `/home/node/.openclaw/` 目录默认不存在，且 entrypoint 不会预先创建。

### 正确做法

在**宿主机**准备好目录和配置文件，通过 Docker Bind Mount 挂载到容器：

```go
// 宿主机目录结构
// data/weclaw-openclaw/{容器名}/openclaw.json

// Docker HostConfig
Binds: []string{hostDir + ":/home/node/.openclaw"}
```

宿主机写入的 `openclaw.json` 内容：
```json
{"gateway":{"controlUi":{"dangerouslyAllowHostHeaderOriginFallback":true}}}
```

### 权限注意

- macOS Docker Desktop: 一般无需额外处理
- Linux: 若容器内报 EACCES，需 `chown -R 1000:1000 <宿主机目录>`

## 5. 容器启动命令

```go
Cmd: []string{"openclaw", "gateway", "--allow-unconfigured", "--bind", "lan"}
```

- `--allow-unconfigured`: 允许在未完成 onboarding wizard 的情况下启动
- `--bind lan`: 绑定到 0.0.0.0，宿主机可通过映射端口访问
- 不要用 `sh -c "..."` 写配置后再 exec，用 bind mount 代替

## 6. 关键环境变量

| 环境变量 | 用途 |
|---------|------|
| `OPENCLAW_GATEWAY_TOKEN` | Gateway 认证 Token |
| `OPENCLAW_GATEWAY_BIND` | 绑定模式 (`lan`/`loopback`) |
| `ANTHROPIC_API_KEY` | Anthropic 模型的 API Key |
| `OPENAI_API_KEY` | OpenAI 模型的 API Key |
| `ANTHROPIC_BASE_URL` | Anthropic API 代理地址（非 `OPENCLAW_BASE_URL`） |
| `NODE_OPTIONS` | Node.js 内存限制 |

**注意**: `OPENCLAW_BASE_URL` 不是 Anthropic SDK 识别的环境变量。如果使用 Anthropic API 代理，应使用 `ANTHROPIC_BASE_URL`。

## 7. Gateway 通信方式（关键发现）

### Gateway 是 WebSocket，不是 HTTP REST

Gateway 启动后的日志：
```
[gateway] listening on ws://0.0.0.0:18789 (PID 14)
```

- **没有** `/v1/responses` 这个 HTTP 端点（返回 404）
- **没有** `/v1/chat/completions`（返回 404）
- HTTP 访问根路径或 `/healthz` 会返回 Control UI 的 HTML 页面
- 实际 API 通过 **WebSocket** 通信

### 发送消息的正确方式

#### 方式一：容器内 CLI（docker exec）

```bash
docker exec <container> openclaw agent \
  --agent main \
  --json \
  -m "你好"
```

返回 JSON 结果，`result.payloads[0].text` 是回复文本。

#### 方式二：WebSocket 连接（待实现）

从 Go 后端通过 WebSocket 连接 `ws://127.0.0.1:{port}`，使用 Gateway Token 认证。适合生产环境。

#### 方式三：ACP (Agent Control Protocol)

```bash
openclaw acp --token <token> --url ws://127.0.0.1:18789
```

### 健康检查的正确方式

HTTP 端点返回 HTML，用 Gateway Call 方式：
```bash
docker exec <container> openclaw gateway call health \
  --token <token> \
  --url ws://127.0.0.1:18789 \
  --json
```

## 8. 模型配置（已验证）

### 关键发现：OpenClaw 忽略 OPENAI_BASE_URL / ANTHROPIC_BASE_URL 环境变量

OpenClaw 的 `Shell env` 默认为 **off**，导致它完全忽略以下环境变量：
- `OPENAI_BASE_URL`
- `ANTHROPIC_BASE_URL`

即使设置了这些环境变量，OpenClaw 仍会直接请求官方 API 地址。

### 正确方案：自定义 Provider（models.providers）

通过 `openclaw.json` 的 `models.providers` 注册自定义 LLM 提供商，在其中指定 `baseUrl` 和 `apiKey`：

```json
{
  "gateway": {
    "controlUi": {
      "dangerouslyAllowHostHeaderOriginFallback": true
    }
  },
  "agents": {
    "defaults": {
      "model": { "primary": "weclaw-llm/claude-sonnet-4-6" },
      "models": { "weclaw-llm/claude-sonnet-4-6": {} }
    }
  },
  "models": {
    "providers": {
      "weclaw-llm": {
        "baseUrl": "https://api.moleapi.com/v1",
        "apiKey": "sk-xxx",
        "api": "openai-completions",
        "models": [
          { "id": "claude-sonnet-4-6", "name": "Claude Sonnet 4.6" }
        ]
      }
    }
  }
}
```

关键字段：
- `api`: `"openai-completions"` 或 `"anthropic-messages"`，决定 SDK 路径
- `baseUrl`: 代理/中转站地址，带 `/v1` 后缀（OpenAI 兼容格式）
- `apiKey`: 直接写在 config 中，无需环境变量
- `models`: 数组，必须提供，每项至少有 `id` 和 `name`

### 模型名验证

```bash
# 查看已知模型
docker exec <container> openclaw models list --all --plain | grep sonnet

# 查看当前配置的模型
docker exec <container> openclaw models
```

## 9. 首次消息 Bootstrap 行为

Agent 首次启动会执行 bootstrap（读取 BOOTSTRAP.md 等），第一次消息可能返回空 payload 或自我介绍内容。后续消息正常。

## 10. 已验证可行的完整链路

1. 用户发消息 → WeClaw Go 后端
2. Go 后端 `docker exec <container> openclaw agent --agent main --json -m <message>`
3. OpenClaw agent → 自定义 provider（OpenAI 兼容代理）→ Claude
4. Claude 回复 → OpenClaw agent → JSON stdout
5. Go 后端解析 `result.payloads[].text` → 回复用户

## 10. 调试常用命令

```bash
# 查看容器日志
docker logs <container> 2>&1

# 进入容器调试
docker exec -it <container> sh

# 查看配置文件路径
docker exec <container> openclaw config file

# 读取某个配置值
docker exec <container> openclaw config get <dot.path>

# 设置配置值
docker exec <container> openclaw config set <dot.path> <value>

# Gateway 健康检查
docker exec <container> openclaw gateway call health --token <token> --url ws://127.0.0.1:18789 --json

# 查看 CLI 版本
docker exec <container> openclaw --version
```
