package router

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/internal/container"
	"github.com/qcy/weclaw/internal/openclaw"
	"github.com/qcy/weclaw/internal/user"
	"github.com/qcy/weclaw/internal/wechat"
	"github.com/qcy/weclaw/pkg/logger"
	"gorm.io/gorm"
)

// MessageRouter routes incoming WeChat messages to the appropriate handler.
type MessageRouter struct {
	userService    *user.Service
	containerMgr   *container.Manager
	openclawClient *openclaw.Client
	wechatAPI      *wechat.API
	cfg            *config.Config
}

// NewMessageRouter creates a new message router.
func NewMessageRouter(
	userService *user.Service,
	containerMgr *container.Manager,
	openclawClient *openclaw.Client,
	wechatAPI *wechat.API,
	cfg *config.Config,
) *MessageRouter {
	return &MessageRouter{
		userService:    userService,
		containerMgr:   containerMgr,
		openclawClient: openclawClient,
		wechatAPI:      wechatAPI,
		cfg:            cfg,
	}
}

// Route processes an incoming WeChat message and returns a reply.
func (r *MessageRouter) Route(msg *wechat.IncomingMessage) *wechat.ReplyMessage {
	switch msg.MsgType {
	case wechat.MsgTypeEvent:
		return r.handleEvent(msg)
	case wechat.MsgTypeText:
		return r.handleText(msg)
	case wechat.MsgTypeVoice:
		// If voice recognition is enabled, use the recognized text
		if msg.Recognition != "" {
			msg.Content = msg.Recognition
			return r.handleText(msg)
		}
		return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
			"🎤 收到语音消息。目前仅支持文字消息，请发送文字与 AI 助手交流。")
	default:
		return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
			"📝 目前仅支持文字消息。请发送文字与 AI 助手交流。")
	}
}

// handleEvent processes WeChat events (subscribe, unsubscribe, etc.).
func (r *MessageRouter) handleEvent(msg *wechat.IncomingMessage) *wechat.ReplyMessage {
	switch msg.Event {
	case wechat.EventSubscribe:
		return r.handleSubscribe(msg)
	case wechat.EventUnsubscribe:
		r.handleUnsubscribe(msg)
		return nil // No reply needed for unsubscribe
	default:
		logger.Info("Unhandled event type", "event", msg.Event, "user", msg.FromUserName)
		return nil
	}
}

// handleSubscribe handles the user subscribe event.
func (r *MessageRouter) handleSubscribe(msg *wechat.IncomingMessage) *wechat.ReplyMessage {
	openID := msg.FromUserName

	// Check if user already exists (re-subscribe)
	existingUser, err := r.userService.FindByOpenID(openID)
	if err == nil && existingUser != nil {
		if existingUser.Status == user.StatusDisabled {
			// Re-enable the user
			_ = r.userService.UpdateStatus(openID, user.StatusActive)

			// Try to restart the container if it still exists
			if existingUser.ContainerID != "" {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel()
					if err := r.containerMgr.StartContainer(ctx, existingUser.ContainerID); err != nil {
						logger.Warn("Failed to restart container for re-subscribed user",
							"user", openID, "error", err)
						// Container might have been removed, create a new one
						r.createContainerForUser(openID)
					}
				}()

				return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
					"🎉 欢迎回来！正在恢复您的 AI 助手...\n\n稍后即可发送消息开始使用。")
			}
		} else {
			return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
				"👋 欢迎回来！直接发送消息即可与 AI 助手交流。")
		}
	}

	// Create new user
	_, err = r.userService.Create(openID)
	if err != nil {
		logger.Error("Failed to create user", "openid", openID, "error", err)
		return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
			"❌ 系统异常，请稍后再试。")
	}

	// Create container asynchronously
	go r.createContainerForUser(openID)

	return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
		"🎉 欢迎关注！正在为您准备专属 AI 助手...\n\n"+
			"⏳ 首次初始化需要约1分钟，请稍候。\n"+
			"准备就绪后，您可以直接发送消息与 AI 助手交流。\n\n"+
			"💡 使用提示：\n"+
			"• 直接发送文字即可与 AI 对话\n"+
			"• AI 可以帮您编程、写文案、回答问题等\n"+
			fmt.Sprintf("• 每天可发送 %d 条消息", r.cfg.Quota.MaxMessagesPerDay))
}

// handleUnsubscribe handles the user unsubscribe event.
func (r *MessageRouter) handleUnsubscribe(msg *wechat.IncomingMessage) {
	openID := msg.FromUserName

	u, err := r.userService.FindByOpenID(openID)
	if err != nil {
		logger.Warn("Unsubscribe: user not found", "openid", openID)
		return
	}

	// Remove container asynchronously
	if u.ContainerID != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := r.containerMgr.RemoveContainer(ctx, u.ContainerID, u.ContainerPort); err != nil {
				logger.Error("Failed to remove container on unsubscribe",
					"user", openID, "container", u.ContainerID[:12], "error", err)
			}
		}()
	}

	// Disable user
	_ = r.userService.Disable(openID)
	logger.Info("User unsubscribed", "openid", openID)
}

// handleText handles text messages from the user.
func (r *MessageRouter) handleText(msg *wechat.IncomingMessage) *wechat.ReplyMessage {
	openID := msg.FromUserName
	content := msg.Content

	// Find user
	u, err := r.userService.FindByOpenID(openID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
				"⚠️ 用户信息未找到，请尝试取消关注后重新关注。")
		}
		logger.Error("Failed to find user", "openid", openID, "error", err)
		return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
			"❌ 系统异常，请稍后再试。")
	}

	// Check user status
	switch u.Status {
	case user.StatusPending:
		return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
			"⏳ AI 助手仍在初始化中，请稍等片刻再试。")
	case user.StatusDisabled:
		return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
			"⚠️ 您的账号已停用。请取消关注后重新关注以重新激活。")
	}

	// Check quota
	allowed, err := r.userService.CheckQuota(openID)
	if err != nil {
		logger.Error("Failed to check quota", "openid", openID, "error", err)
	}
	if !allowed {
		return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
			fmt.Sprintf("⚠️ 今日消息已达上限 (%d 条)，请明天再来使用。",
				r.cfg.Quota.MaxMessagesPerDay))
	}

	// Log incoming message
	_ = r.userService.LogMessage(u.ID, "incoming", string(msg.MsgType), content)

	// Increment message count and touch activity
	_ = r.userService.IncrementMsgCount(openID)
	_ = r.userService.TouchActivity(openID)

	// Handle container wake-up if sleeping
	if u.Status == user.StatusSleeping {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := r.containerMgr.StartContainer(ctx, u.ContainerID); err != nil {
			if strings.Contains(err.Error(), "No such container") {
				logger.Warn("Container missing on wakeup, recreating...", "user", openID)
				go r.createContainerForUser(openID)
				return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
					"🔄 发现您的 AI 助手状态异常，正在为您重新初始化，请稍后近1分钟时再次发送消息。")
			}
			logger.Error("Failed to wake up container", "user", openID, "error", err)
			return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
				"❌ AI 助手唤醒失败，请稍后再试。")
		}
		_ = r.userService.UpdateStatus(openID, user.StatusActive)

		// Wait a bit for container to be ready
		time.Sleep(3 * time.Second)
	}

	// Send message to OpenClaw asynchronously and reply via customer service API
	go r.processAndReply(u, content, msg.ToUserName)

	// Return immediate passive reply
	return wechat.NewTextReply(msg.FromUserName, msg.ToUserName,
		"💬 收到！正在思考中... ⏳")
}

// processAndReply sends the message to OpenClaw and pushes the result via customer service API.
func (r *MessageRouter) processAndReply(u *user.User, content, appOpenID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Send to OpenClaw
	response, err := r.openclawClient.SendMessage(ctx, u.ContainerName, content)
	if err != nil {
		logger.Error("Failed to get OpenClaw response",
			"user", u.OpenID, "error", err)

		// Try to notify user of error via customer service API
		_ = r.wechatAPI.SendTextMessage(u.OpenID,
			"❌ AI 处理请求时出错，请稍后再试。\n错误信息: "+err.Error())
		return
	}

	// Format for WeChat
	formatted := openclaw.FormatForWeChat(response)

	// Split long messages
	parts := openclaw.SplitLongMessage(formatted, openclaw.MaxWeChatTextLength)

	// Send via customer service API
	for _, part := range parts {
		if err := r.wechatAPI.SendTextMessage(u.OpenID, part); err != nil {
			logger.Error("Failed to send reply via customer service API",
				"user", u.OpenID, "error", err)
		}
		// Small delay between parts to maintain order
		if len(parts) > 1 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Log outgoing message
	_ = r.userService.LogMessage(u.ID, "outgoing", "text", response)
}

// createContainerForUser creates an OpenClaw container for a user.
func (r *MessageRouter) createContainerForUser(openID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	info, err := r.containerMgr.CreateContainer(ctx, openID, &r.cfg.OpenClaw)
	if err != nil {
		logger.Error("Failed to create container for user",
			"user", openID, "error", err)
		return
	}

	// Update user with container info
	if err := r.userService.UpdateContainerInfo(
		openID,
		info.ContainerID,
		info.ContainerName,
		info.Port,
		info.GatewayToken,
	); err != nil {
		logger.Error("Failed to update user with container info",
			"user", openID, "error", err)
		return
	}

	logger.Info("Container ready for user",
		"user", openID,
		"container", info.ContainerID[:12],
		"port", info.Port,
	)

	// Notify user via customer service API
	_ = r.wechatAPI.SendTextMessage(openID,
		"✅ 您的 AI 助手已准备就绪！\n\n直接发送消息即可开始对话。")
}
