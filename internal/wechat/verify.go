package wechat

import (
	"crypto/sha1"
	"fmt"
	"sort"
	"strings"
)

// VerifySignature verifies the WeChat server callback signature.
// WeChat sends signature=sha1(sort(token, timestamp, nonce))
func VerifySignature(token, signature, timestamp, nonce string) bool {
	strs := []string{token, timestamp, nonce}
	sort.Strings(strs)
	combined := strings.Join(strs, "")

	hash := sha1.New()
	hash.Write([]byte(combined))
	expected := fmt.Sprintf("%x", hash.Sum(nil))

	return expected == signature
}
