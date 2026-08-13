package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
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
	var previewBuffer bytes.Buffer
	previewImage := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	previewImage.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	if err := png.Encode(&previewBuffer, previewImage); err != nil {
		t.Fatal(err)
	}
	preview := previewBuffer.Bytes()
	if err := notifier.Notify(context.Background(), PortNotification{
		PackID: "0123456789abcdef", OutputJSON: output, PreviewPNG: preview,
	}); err != nil {
		t.Fatal(err)
	}
	if gotContent != string(output) {
		t.Fatalf("webhook content = %q, want only %q", gotContent, output)
	}
	labeledPreview, err := png.Decode(bytes.NewReader(gotPreview))
	if err != nil {
		t.Fatalf("decode labeled preview: %v", err)
	}
	if labeledPreview.Bounds().Dx() != 4 || labeledPreview.Bounds().Dy() != 4+previewIDFooter {
		t.Fatalf("labeled preview size = %v", labeledPreview.Bounds().Size())
	}
}

func TestDiscordNotifierRejectsOversizedMessage(t *testing.T) {
	notifier, err := NewDiscordNotifier("https://discord.com/api/webhooks/test/test")
	if err != nil {
		t.Fatal(err)
	}
	err = notifier.Notify(context.Background(), PortNotification{
		PackID: "pack", OutputJSON: []byte(strings.Repeat("x", discordMessageLimit+1)), PreviewPNG: []byte("png"),
	})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized message error = %v", err)
	}
}
