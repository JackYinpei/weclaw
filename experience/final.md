AI 回复了！ 响应在 result.payloads[0].text 中，格式和客户端代码预期的完全一致。第一次调用是 bootstrap（"Who am I?"），之后的消息就正常了。

> 注意：2026-03-09 架构重构后已移除微信公众号和 test_api.go，容器管理通过 Web Dashboard 和 `/api/containers` 系列 API 完成。以下为早期调试过程的历史记录。

之前测试脚本返回"无响应内容"说明 docker exec 的 exit code 可能是非 0（被 client.go 当成 error），或者输出包含了 stderr。让我检查 client.go 的解析逻辑：

完美！ raw_response 是 "测试通过"，wechat_formatted 也是 "测试通过"！整条链路完全跑通了！

之前测试脚本首次消息为空是因为 agent 的 bootstrap 阶段（读取 BOOTSTRAP.md 等工具调用）不产生文本 payload。后续消息就正常了。

清理并更新经验文档：

更新经验文档，加入今天发现的关键信息：

openclaw-docker-integration.md
+51
-9
## 8. 模型配置（待完善）OpenClaw 默认使用 `claude-opus-4-6` 模型。要修改为其他模型需在 `openclaw.json` 中配置：
## 8. 模型配置（待完善）OpenClaw 默认使用 `claude-opus-4-6` 模型。要修改为其他模型需在 `openclaw.json` 中配置：## 8. 模型配置（已验证）
整条链路现在完全跑通了。总结一下今天完成的工作：

成果
从用户发消息到 AI 回复的完整链路已打通：

用户消息 → WeClaw API → docker exec openclaw agent → 自定义 Provider (moleapi) → Claude Sonnet 4.6 → "测试通过"
解决的核心问题
问题	根因	解决方案
配置写入失败	容器内 /home/node/.openclaw/ 不存在	宿主机 bind mount
配置文件名错误	应为 openclaw.json，非 config.json5	修正文件名
Gateway 无 HTTP REST API	Gateway 是 WebSocket，/v1/responses 返回 404	改用 docker exec openclaw agent
SDK 忽略环境变量	OpenClaw Shell env = off，忽略 *_BASE_URL	自定义 Provider (models.providers) 写入 openclaw.json
API Key 401	请求发到了 Anthropic 官方而非代理	自定义 Provider 的 baseUrl + api: "openai-completions"
修改的文件
internal/container/manager.go — bind mount + openclaw.json 生成（含自定义 provider）
internal/openclaw/client.go — 改用 docker exec openclaw agent --json 发消息
internal/api/test_api.go / internal/router/router.go — 更新 SendMessage 调用签名
internal/config/config.go — 新增 OpenClawHostDataDir 配置
configs/config.yaml — model_provider / base_url / host_data_dir
scripts/test.sh — Gateway 启动等待时间从 10s 改为 40s
experience/openclaw-docker-integration.md — 经验文档更新