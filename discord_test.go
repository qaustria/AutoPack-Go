package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscordNotifierSendsMessageAndHotbarPreview(t *testing.T) {
	var gotContent string
	var gotPreview []byte
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("webhook method = %s", request.Method)
		}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse webhook multipart: %v", err)
			http.Error(response, "bad multipart", http.StatusBadRequest)
			return
		}
		var payload struct {
			Content         string `json:"content"`
			AllowedMentions struct {
				Parse []string `json:"parse"`
			} `json:"allowed_mentions"`
		}
		if err := json.Unmarshal([]byte(request.FormValue("payload_json")), &payload); err != nil {
			t.Errorf("decode webhook payload: %v", err)
		}
		gotContent = payload.Content
		if len(payload.AllowedMentions.Parse) != 0 {
			t.Errorf("webhook allows mentions: %#v", payload.AllowedMentions.Parse)
		}
		file, _, err := request.FormFile("files[0]")
		if err != nil {
			t.Errorf("read preview attachment: %v", err)
			return
		}
		defer file.Close()
		gotPreview, err = io.ReadAll(file)
		if err != nil {
			t.Errorf("read preview bytes: %v", err)
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	notifier, err := NewDiscordNotifier(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	notifier.client = server.Client()
	output := []byte(`{"m":null,"t":"buffer","zbase64":"abc"}`)
	preview := []byte("preview-png")
	if err := notifier.Notify(context.Background(), PortNotification{
		PackID: "0123456789abcdef", OutputJSON: output, PreviewPNG: preview,
	}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"**:exclamation: A Pack has been ported!**",
		"Pack ID: `0123456789abcdef`",
		"```json\n" + string(output) + "\n```",
	} {
		if !strings.Contains(gotContent, expected) {
			t.Fatalf("webhook content does not contain %q: %s", expected, gotContent)
		}
	}
	if string(gotPreview) != string(preview) {
		t.Fatalf("preview attachment = %q, want %q", gotPreview, preview)
	}
}

func TestDiscordNotifierRejectsOversizedMessage(t *testing.T) {
	notifier, err := NewDiscordNotifier("https://discord.com/api/webhooks/test/test")
	if err != nil {
		t.Fatal(err)
	}
	err = notifier.Notify(context.Background(), PortNotification{
		PackID: "pack", OutputJSON: []byte(strings.Repeat("x", discordMessageLimit)), PreviewPNG: []byte("png"),
	})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized message error = %v", err)
	}
}
