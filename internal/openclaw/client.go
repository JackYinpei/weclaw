package openclaw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/qcy/weclaw/pkg/logger"
)

// Client communicates with an OpenClaw Gateway instance via docker exec.
type Client struct {
	timeout time.Duration
}

// NewClient creates a new OpenClaw API client.
func NewClient() *Client {
	return &Client{
		timeout: 120 * time.Second,
	}
}

// agentResult is the JSON structure returned by `openclaw agent --json`.
type agentResult struct {
	RunID   string `json:"runId"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Result  *struct {
		Payloads []struct {
			Text     string  `json:"text"`
			MediaURL *string `json:"mediaUrl"`
		} `json:"payloads"`
		Meta *struct {
			DurationMs int  `json:"durationMs"`
			Aborted    bool `json:"aborted"`
		} `json:"meta"`
	} `json:"result"`
}

// SendMessage sends a text message to an OpenClaw container and returns the response.
// It calls `docker exec <containerName> openclaw agent --agent main --json -m <message>`.
func (c *Client) SendMessage(ctx context.Context, containerName string, message string) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args := []string{
		"exec", containerName,
		"openclaw", "agent",
		"--agent", "main",
		"--json",
		"-m", message,
	}

	logger.Debug("Sending message to OpenClaw via docker exec",
		"container", containerName,
		"message_preview", truncate(message, 50),
	)

	cmd := exec.CommandContext(execCtx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return "", fmt.Errorf("docker exec failed: %w, stderr: %s", err, stderrStr)
		}
		return "", fmt.Errorf("docker exec failed: %w", err)
	}

	output := stdout.Bytes()

	var result agentResult
	if err := json.Unmarshal(output, &result); err != nil {
		return "", fmt.Errorf("failed to parse openclaw agent output: %w, raw: %s", err, truncate(string(output), 200))
	}

	if result.Status != "ok" {
		return "", fmt.Errorf("openclaw agent returned status=%s summary=%s", result.Status, result.Summary)
	}

	if result.Result == nil || len(result.Result.Payloads) == 0 {
		return "[无响应内容]", nil
	}

	var texts []string
	for _, p := range result.Result.Payloads {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	if len(texts) == 0 {
		return "[无响应内容]", nil
	}

	text := strings.Join(texts, "\n")

	logger.Debug("Received response from OpenClaw",
		"container", containerName,
		"response_preview", truncate(text, 100),
		"duration_ms", result.Result.Meta.DurationMs,
	)

	return text, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
