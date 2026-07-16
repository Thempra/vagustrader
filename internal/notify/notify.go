package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type Notifier struct {
	token     string
	channelID string
	client    *http.Client
}

func New(botToken, channelID string) *Notifier {
	if botToken == "" || channelID == "" {
		return nil
	}
	return &Notifier{
		token:     botToken,
		channelID: channelID,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (n *Notifier) Send(text string) {
	if n == nil {
		return
	}
	payload := map[string]string{
		"chat_id":    n.channelID,
		"text":       text,
		"parse_mode": "HTML",
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.token)
	resp, err := n.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Warn("Telegram send error", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		slog.Warn("Telegram send failed", "status", resp.StatusCode)
	}
}
