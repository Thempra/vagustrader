package clob

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

func BuildHMACSignature(secret string, timestamp int64, method, requestPath, body string) string {
	decoded, err := base64.URLEncoding.DecodeString(secret)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(secret)
		if err != nil {
			decoded, err = base64.RawURLEncoding.DecodeString(secret)
			if err != nil {
				decoded, err = base64.RawStdEncoding.DecodeString(secret)
				if err != nil {
					return ""
				}
			}
		}
	}
	message := fmt.Sprintf("%d%s%s", timestamp, method, requestPath)
	if body != "" {
		message += strings.ReplaceAll(body, "'", `"`)
	}
	mac := hmac.New(sha256.New, decoded)
	mac.Write([]byte(message))
	return base64.URLEncoding.EncodeToString(mac.Sum(nil))
}
