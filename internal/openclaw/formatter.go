package openclaw

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// MaxWeChatTextLength is the maximum length of a single WeChat text message.
	MaxWeChatTextLength = 2000
)

// FormatForWeChat converts Markdown-formatted text from OpenClaw
// into WeChat-compatible plain text.
func FormatForWeChat(markdown string) string {
	result := markdown

	// Remove code block language specifiers (```python → ```)
	result = regexp.MustCompile("```\\w+").ReplaceAllString(result, "```")

	// Convert bold **text** → 【text】
	result = regexp.MustCompile(`\*\*(.*?)\*\*`).ReplaceAllString(result, "【$1】")

	// Convert italic *text* or _text_ → text (just remove markers)
	result = regexp.MustCompile(`(?:^|[^*])\*([^*]+)\*(?:[^*]|$)`).ReplaceAllString(result, "$1")
	result = regexp.MustCompile(`_([^_]+)_`).ReplaceAllString(result, "$1")

	// Convert headers # Heading → 〖Heading〗
	result = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`).ReplaceAllString(result, "〖$1〗")

	// Convert bullet points - item → • item
	result = regexp.MustCompile(`(?m)^[-*]\s+`).ReplaceAllString(result, "• ")

	// Convert numbered lists (keep as-is, they work in plain text)

	// Convert links [text](url) → text (url)
	result = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`).ReplaceAllString(result, "$1 ($2)")

	// Convert inline code `code` → 「code」
	result = regexp.MustCompile("`([^`]+)`").ReplaceAllString(result, "「$1」")

	// Remove horizontal rules ---
	result = regexp.MustCompile(`(?m)^---+$`).ReplaceAllString(result, "————————")

	// Clean up excessive blank lines
	result = regexp.MustCompile(`\n{3,}`).ReplaceAllString(result, "\n\n")

	return strings.TrimSpace(result)
}

// SplitLongMessage splits a long message into multiple WeChat-compatible messages.
func SplitLongMessage(text string, maxLen int) []string {
	if maxLen <= 0 {
		maxLen = MaxWeChatTextLength
	}

	if utf8.RuneCountInString(text) <= maxLen {
		return []string{text}
	}

	var parts []string
	runes := []rune(text)
	total := len(runes)
	partNum := 1

	for i := 0; i < total; {
		end := i + maxLen - 20 // Reserve space for segment indicator
		if end > total {
			end = total
		}

		// Try to split at a natural boundary (newline)
		if end < total {
			for j := end; j > i+maxLen/2; j-- {
				if runes[j] == '\n' {
					end = j + 1
					break
				}
			}
		}

		segment := string(runes[i:end])

		// Add segment indicator if split into multiple parts
		if total > maxLen {
			totalParts := (total + maxLen - 21) / (maxLen - 20)
			segment = segment + fmt.Sprintf("\n\n📄 (%d/%d)", partNum, totalParts)
			partNum++
		}

		parts = append(parts, strings.TrimSpace(segment))
		i = end
	}

	return parts
}
