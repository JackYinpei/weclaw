# Fix Docker Container Panic and Timeouts

> 注意：2026-03-09 架构重构后已移除微信公众号相关代码。容器命名方式改为 `weclaw-openclaw-acct{accountID}-ctr{containerID}`，不再使用 OpenID 前缀。以下踩坑记录仍有参考价值。

我们在使用 OpenClaw 做集成测试过程中遇到了两个主要问题：一个是在创建容器时触发的 Go runtime slice bounds out of range `panic`，以及在发送消息时遇到的 `openclaw agent returned status= summary=` 的超时空响应错误。

## 问题 1：用户 OpenID 截取引发的 Panic
### 现象
前端输入一个较短的 OpenID（例如 `demo-user-1`，长度为11）后，无法成功创建容器。
后端启动日志中出现 `runtime error: slice bounds out of range [:12] with length 11` 宕机错误：
```go
2026/03/06 22:43:57 [Recovery] 2026/03/06 - 22:43:57 panic recovered:
runtime error: slice bounds out of range [:12] with length 11
/Users/qcy/proj/weclaw/internal/container/manager.go:130 (0x102d12407)
        (*Manager).CreateContainer: containerName := fmt.Sprintf("weclaw-openclaw-%s", userOpenID[:12])
```
这不仅中断了正在跑的容器创建挂载流程，而且残缺地把这个用户状态记录为了 `pending`，导致之后所有的调用都出现卡死或“找不到容器”报错。

### 原因及修复
- **原因**：因为生成容器名时想利用用户 ID 的前 12 位，直接用了 `userOpenID[:12]` 导致硬编码的数组越界。
- **解决**：在 `internal/container/manager.go` 中，安全地对字符串进行长度判断并截取：
  ```go
  idPrefix := userOpenID
  if len(idPrefix) > 12 {
      idPrefix = idPrefix[:12]
  }
  containerName := fmt.Sprintf("weclaw-openclaw-%s", idPrefix)
  ```

## 问题 2：API 测试空响应、卡长达 1 分钟超时
### 现象
通过 `api/test/send` 给容器发送请求很久没响应。
经过 1分10秒多后打印如下报错日志：
```json
{
    "elapsed": "1m10.599355s",
    "error": "OpenClaw error: openclaw agent returned status= summary=",
    ...
}
```

### 原因及解决
- **原因**：这不是 WeClaw 或 Go 逻辑的问题。这是在执行 `docker exec <container-name> openclaw agent --agent main --json -m "ping"` 这条命令与代理的模型节点建立通信连接时遭遇了长时间的网络等待与拥塞处理。
  因为我们设置了 `timeout=120s` 之类的时间控制，当遇到上游模型处理缓慢，或者大载荷读取超时，命令行工具输出截断，便无法正常解析出完整的由 JSON 构建的 `{"status": "ok", "summary": "completed"}`。
- **排查验证方法**：遇到这种错误时，可以直接进入容器模拟相同指令查看真实日志输出。
  ```bash
  docker exec 容器名 openclaw agent --agent main --json -m "ping"
  ```
- **解决**：通常这种阻塞是由第三方 LLM 服务偶尔抖动造成的卡死。只要其自身恢复稳定，测试很快就能通过，响应在 7~15 秒以内。并且，在后续如果此类错误频繁，应监控底层的网络出口质量或尝试设置较短时间的上游重试参数。

## 相关衍生操作经验记录
- 如果遇到端口被“幽灵”进程抢占的报错 (`bind: address already in use`)，在尝试用文本替换结束或者 `ps aux | grep weclaw` 无果时，可以使用最保险的 `lsof -i :占用端口号` 找到真实的 `PID` 强制杀掉进程（`kill -9 <PID>`）。
- 测试中因异常退出导致的数据库冗余状态（例如 pending 却无实际 Docker 进程的情况），可通过 Web Dashboard 点击容器卡片上的 **Delete** 按钮触发数据库级联清理（Container 记录 + 关联的 UserSkill/UserMCP/MessageLog 全部删除）。
