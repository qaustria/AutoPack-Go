package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const discordMessageLimit = 2000

type PortNotification struct {
	PackID     string
	PackName   string
	OutputJSON []byte
	PreviewPNG []byte
}

type PortNotifier interface {
	Notify(context.Context, PortNotification) error
}

type DiscordNotifier struct {
	webhookURL string
	client     *http.Client
}

func NewDiscordNotifier(webhookURL string) (*DiscordNotifier, error) {
	webhookURL = strings.TrimSpace(webhookURL)
	if webhookURL == "" {
		return nil, errors.New("Discord webhook URL is empty")
	}
	parsed, err := url.Parse(webhookURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, errors.New("Discord webhook URL is invalid")
	}
	return &DiscordNotifier{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (notifier *DiscordNotifier) Notify(ctx context.Context, notification PortNotification) error {
	if notifier == nil {
		return errors.New("Discord notifier is nil")
	}
	if ctx == nil {
		return errors.New("Discord notification context is nil")
	}
	if notification.PackID == "" || len(notification.OutputJSON) == 0 || len(notification.PreviewPNG) == 0 {
		return errors.New("Discord notification is incomplete")
	}
	content := fmt.Sprintf("**:exclamation: A Pack has been ported!**\nPack ID: `%s`\n```json\n%s\n```", notification.PackID, notification.OutputJSON)
	if len(content) > discordMessageLimit {
		return fmt.Errorf("compressed pack JSON is too large for one Discord message (%d characters)", len(content))
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	payload, err := json.Marshal(map[string]any{
		"content": content,
		"allowed_mentions": map[string]any{
			"parse": []string{},
		},
	})
	if err != nil {
		return fmt.Errorf("encode Discord webhook payload: %w", err)
	}
	payloadPart, err := writer.CreateFormField("payload_json")
	if err != nil {
		return fmt.Errorf("create Discord payload field: %w", err)
	}
	if _, err := payloadPart.Write(payload); err != nil {
		return fmt.Errorf("write Discord payload: %w", err)
	}
	previewPart, err := writer.CreateFormFile("files[0]", "cone-hotbar-"+notification.PackID+".png")
	if err != nil {
		return fmt.Errorf("create Discord preview attachment: %w", err)
	}
	if _, err := previewPart.Write(notification.PreviewPNG); err != nil {
		return fmt.Errorf("write Discord preview attachment: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish Discord webhook body: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, notifier.webhookURL, &body)
	if err != nil {
		return fmt.Errorf("create Discord webhook request: %w", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := notifier.client.Do(request)
	if err != nil {
		return fmt.Errorf("send Discord webhook: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		return fmt.Errorf("Discord webhook returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	return nil
}
