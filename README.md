# WeClaw (微信 + OpenClaw 网关)

WeClaw 是一个高性能的 Golang 后端网关桥接服务。它专门设计用于**连接微信公众号平台和个人专属的 OpenClaw 本地化大模型容器**。

借助 WeClaw，任何人在关注你的微信公众号后，都会在你的服务器上全自动拉起并隔离分配一台跑着 OpenClaw 的 Docker 专属”大脑容器”。用户只需在微信里发一句文字，即可和自己的大模型 AI 助手展开持久的、个性化的智能对话。

## ✨ 核心特性

- 🚀 **全自动生命周期管理**。通过微信监听并注册用户状态，全自动为新用户在后台初始化建立他们相互独立的 OpenClaw AI 大脑。
- 🛡️ **高安全隔离与自愈机制**。每个用户容器彼此隔离，底层配备 JWT 鉴权和系统状态监测引擎。若用户因某些问题被异常释放容器，下一次沟通会自动进行热重载恢复（自愈）。
- 📉 **动态资源优化**。采用”非活动睡眠”技术，当微信用户长达 30 分钟不发消息时，将自动将其专属 OpenClaw 守护进程转入睡眠休眠（不清除数据状态，仅挂起），在下次聊天时瞬间热启动（Wakeup），极大节省服务器算力与内存池资源。
- 🧩 **Skills & MCP 商店**。内置 Skill 和 MCP Server 商店系统，管理员可上架扩展能力，用户通过 Web 面板一键安装/卸载，动态注入到各自的 OpenClaw 容器中，无需重建容器。
- 📚 **共享知识库**。通过全局 Docker Named Volume 挂载只读的共享文档目录到所有容器，让全部用户的 AI 助手都能读取统一的知识资料。
- ⚙️ **ChatGPT 风格三栏 Web 控制面板**。左侧可折叠侧栏管理已安装的 Skills/MCP，中间商店一键安装，右侧实时对话测试。

## 📸 系统预览图

<div align="center">
    <img src="web/image1.png" alt="WeClaw 控制面板演示 - 登录认证界面" width="48%">
    <img src="web/image2.png" alt="WeClaw 控制面板演示 - 面板工作台及聊天模拟器" width="48%">
</div>

## 🔧 快速启动

1. **环境准备：** 确保当前系统安装 Go 1.25.0+ 以及拥有操作 Docker 的权限。
2. **初始化配置文件的环境变量：** （支持编辑 `configs/config.yaml` 或者将诸如 `WECLAW_OPENCLAW_API_KEY` 等直接写入 Linux）。
3. **运行或编译项目：**
   ```console
   # 直接开发调试启动
   go run cmd/weclaw/main.go
   
   # 或者先构建在运行
   go build -o bin/weclaw cmd/weclaw/main.go
   ./bin/weclaw
   ```
4. **访问控制管理面板：**
   通过浏览器访问 `http://127.0.0.1:8080/web/index.html`。
   > _提示：第一次访问时需要向后端注册或填写你的管理员凭证以登录工作台，之后会自动用 Token 验证调用内部系统容器_。

## 📖 面向开发人员与贡献者
查看我们总结的第一手技术防坑和踩坑开发指南，请参阅本工程中的：[CLAUDE.md](./CLAUDE.md)

我们解决了很多如：JSON配置映射异常、环境变量被刻意忽略、并发网络死锁以及 Docker 回收引发越界宕机等大量疑难杂症。
详细探索各次 Bug 定位与解题历史，可跳转查看 `experience/` 目录下相关的笔记。

## 🧩 Skills & MCP 商店使用

### 管理员：上架商品

通过 API 向商店添加 Skill 或 MCP Server：

```bash
# 添加 Skill 到商店
curl -X POST http://localhost:8080/api/store/skills \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"name":"web-search","display_name":"Web Search","description":"Enable web search capability","category":"search","icon":"🔍"}'

# 添加 MCP Server 到商店
curl -X POST http://localhost:8080/api/store/mcps \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"name":"tavily","display_name":"Tavily Search","description":"AI search engine","category":"search","icon":"🌐","command":"npx","args":"[\"-y\",\"tavily-mcp@latest\"]","default_env":"{\"TAVILY_API_KEY\":\"your-key\"}"}'
```

### 用户：安装并应用

1. 打开 Web 面板 → 点击左侧栏 **Store** 按钮
2. 在商店中选择需要的 Skill/MCP → 点击 **Install**
3. 点击左侧栏底部的 **Apply Changes** 按钮
4. 系统自动重写 `openclaw.json` 并重启容器

### 共享知识库

管理员直接将文件放入宿主机 `./data/shared-knowledge/` 目录即可：

```bash
# 放置共享文档
cp -r /path/to/docs/* ./data/shared-knowledge/
```

所有用户容器以只读方式在 `/home/node/shared-knowledge` 路径下访问这些文件。系统会自动写入 `AGENTS.md` 告知 OpenClaw agent 该路径，使其纳入系统提示词。

Web 控制面板左侧栏会展示知识库文件树，点击文件可直接查看内容。
