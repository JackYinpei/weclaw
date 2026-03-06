package wechat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/pkg/logger"
)

// API provides access to WeChat Official Account APIs.
type API struct {
	cfg        *config.WeChatConfig
	httpClient *http.Client

	// Access token cache
	mu          sync.RWMutex
	accessToken string
	tokenExpiry time.Time
}

// NewAPI creates a new WeChat API client.
func NewAPI(cfg *config.WeChatConfig) *API {
	return &API{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// tokenResponse represents the response from the access_token API.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
}

// GetAccessToken returns a valid access token, refreshing if necessary.
func (a *API) GetAccessToken() (string, error) {
	a.mu.RLock()
	if a.accessToken != "" && time.Now().Before(a.tokenExpiry) {
		token := a.accessToken
		a.mu.RUnlock()
		return token, nil
	}
	a.mu.RUnlock()

	// Need to refresh token
	a.mu.Lock()
	defer a.mu.Unlock()

	// Double-check after acquiring write lock
	if a.accessToken != "" && time.Now().Before(a.tokenExpiry) {
		return a.accessToken, nil
	}

	url := fmt.Sprintf(
		"https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid=%s&secret=%s",
		a.cfg.AppID, a.cfg.AppSecret,
	)

	resp, err := a.httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to get access token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read token response: %w", err)
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse token response: %w", err)
	}

	if tokenResp.ErrCode != 0 {
		return "", fmt.Errorf("WeChat API error: %d - %s", tokenResp.ErrCode, tokenResp.ErrMsg)
	}

	a.accessToken = tokenResp.AccessToken
	// Refresh 5 minutes before expiry
	a.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn-300) * time.Second)

	logger.Info("Access token refreshed", "expires_in", tokenResp.ExpiresIn)
	return a.accessToken, nil
}

// customerServiceMessage represents a customer service message to send.
type customerServiceMessage struct {
	ToUser  string       `json:"touser"`
	MsgType string       `json:"msgtype"`
	Text    *textContent `json:"text,omitempty"`
}

type textContent struct {
	Content string `json:"content"`
}

// SendTextMessage sends a text message to a user via the customer service API.
// Note: This requires the account to have customer service API permission (认证订阅号或服务号).
func (a *API) SendTextMessage(toUser, content string) error {
	token, err := a.GetAccessToken()
	if err != nil {
		return fmt.Errorf("failed to get access token: %w", err)
	}

	msg := customerServiceMessage{
		ToUser:  toUser,
		MsgType: "text",
		Text:    &textContent{Content: content},
	}

	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	url := fmt.Sprintf("https://api.weixin.qq.com/cgi-bin/message/custom/send?access_token=%s", token)

	resp, err := a.httpClient.Post(url, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("failed to send customer service message: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if result.ErrCode != 0 {
		return fmt.Errorf("WeChat API error: %d - %s", result.ErrCode, result.ErrMsg)
	}

	logger.Debug("Customer service message sent", "to", toUser)
	return nil
}
