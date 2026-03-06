package wechat

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/pkg/logger"
)

// MessageProcessor is an interface that processes incoming WeChat messages
// and returns a reply. This breaks the import cycle between wechat and router packages.
type MessageProcessor interface {
	Route(msg *IncomingMessage) *ReplyMessage
}

// Handler handles WeChat HTTP requests.
type Handler struct {
	cfg       *config.WeChatConfig
	processor MessageProcessor
}

// NewHandler creates a new WeChat handler.
func NewHandler(cfg *config.WeChatConfig, processor MessageProcessor) *Handler {
	return &Handler{
		cfg:       cfg,
		processor: processor,
	}
}

// RegisterRoutes registers WeChat routes on the gin engine.
func (h *Handler) RegisterRoutes(r *gin.Engine) {
	r.GET("/wechat", h.Verify)
	r.POST("/wechat", h.HandleMessage)
}

// Verify handles the WeChat server verification (GET request).
// WeChat sends: signature, timestamp, nonce, echostr
// We must verify the signature and return echostr if valid.
func (h *Handler) Verify(c *gin.Context) {
	signature := c.Query("signature")
	timestamp := c.Query("timestamp")
	nonce := c.Query("nonce")
	echostr := c.Query("echostr")

	logger.Info("WeChat verification request",
		"signature", signature,
		"timestamp", timestamp,
		"nonce", nonce,
	)

	if VerifySignature(h.cfg.Token, signature, timestamp, nonce) {
		logger.Info("WeChat verification successful")
		c.String(http.StatusOK, echostr)
	} else {
		logger.Warn("WeChat verification failed", "expected_token", h.cfg.Token)
		c.String(http.StatusForbidden, "verification failed")
	}
}

// HandleMessage handles incoming WeChat messages (POST request).
func (h *Handler) HandleMessage(c *gin.Context) {
	// Verify signature first
	signature := c.Query("signature")
	timestamp := c.Query("timestamp")
	nonce := c.Query("nonce")

	if !VerifySignature(h.cfg.Token, signature, timestamp, nonce) {
		logger.Warn("Message signature verification failed")
		c.String(http.StatusForbidden, "verification failed")
		return
	}

	// Read request body
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.Error("Failed to read request body", "error", err)
		c.String(http.StatusBadRequest, "bad request")
		return
	}
	defer c.Request.Body.Close()

	// Parse incoming message
	msg, err := ParseMessage(body)
	if err != nil {
		logger.Error("Failed to parse WeChat message", "error", err)
		c.String(http.StatusBadRequest, "bad request")
		return
	}

	logger.Info("Received WeChat message",
		"from", msg.FromUserName,
		"type", msg.MsgType,
		"event", msg.Event,
	)

	// Route message and get reply
	reply := h.processor.Route(msg)

	if reply == nil {
		// Return empty response (tells WeChat we received it successfully)
		c.String(http.StatusOK, "success")
		return
	}

	// Marshal and return reply
	replyData, err := MarshalReply(reply)
	if err != nil {
		logger.Error("Failed to marshal reply", "error", err)
		c.String(http.StatusOK, "success")
		return
	}

	c.Data(http.StatusOK, "application/xml", replyData)
}
