package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubWebProcessor struct {
	filename string
	payload  string
}

type capturedCredentials struct {
	apiKey string
	userID string
}

type stubPortNotifier struct {
	notification PortNotification
}

func (notifier *stubPortNotifier) Notify(_ context.Context, notification PortNotification) error {
	notifier.notification = notification
	return nil
}

func (processor *stubWebProcessor) ProcessUpload(_ context.Context, filename string, upload io.Reader, progress ProgressFunc) (PipelineResult, error) {
	data, err := io.ReadAll(upload)
	if err != nil {
		return PipelineResult{}, err
	}
	processor.filename = filename
	processor.payload = string(data)
	progress(ProgressEvent{Stage: ProgressReceiving, Message: "Receiving test.zip"})
	progress(ProgressEvent{Stage: ProgressUploading, Completed: 1, Total: 1, Name: "Cone test mesh", Message: "Uploaded Cone test mesh"})
	progress(ProgressEvent{Stage: ProgressComplete, Completed: 1, Total: 1, Message: "Generated JSON mapping"})
	return PipelineResult{
		Values: map[string]any{
			"SwordMesh": "123", "ShearsRotation": []int{0, 0, -35},
		},
		PackID: "0123456789abcdef", PackName: filename, PreviewPNG: []byte("preview"),
	}, nil
}

func TestWebHandlerServesFrontendWithSecurityHeaders(t *testing.T) {
	handler, err := NewWebHandler(&stubWebProcessor{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "Drop texture pack here") {
		t.Fatalf("frontend body is unexpected: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `id="activity-log"`) {
		t.Fatal("frontend is missing its live porting log")
	}
	if !strings.Contains(response.Body.String(), `id="json-output"`) || !strings.Contains(response.Body.String(), `id="copy-button"`) {
		t.Fatal("frontend is missing its on-page JSON output or copy button")
	}
	if !strings.Contains(response.Body.String(), `id="api-key-input"`) || !strings.Contains(response.Body.String(), `id="user-id-input"`) || !strings.Contains(response.Body.String(), "Assets: Read + Write") {
		t.Fatal("frontend is missing Roblox credential fields or API-key instructions")
	}
	if !strings.Contains(response.Body.String(), "Leave IP restriction off.") || !strings.Contains(response.Body.String(), `id="remember-credentials"`) {
		t.Fatal("frontend is missing its IP instruction or browser credential-memory control")
	}
	if !strings.Contains(response.Body.String(), "Cone is fully open-source.") {
		t.Fatal("frontend is missing its open-source notice")
	}
	if strings.Contains(response.Body.String(), "Download JSON") {
		t.Fatal("frontend still offers a JSON download instead of showing the result")
	}
	if !strings.Contains(response.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal("frontend is missing its content security policy")
	}

	iconRequest := httptest.NewRequest(http.MethodGet, "/cone.png", nil)
	iconResponse := httptest.NewRecorder()
	handler.ServeHTTP(iconResponse, iconRequest)
	if iconResponse.Code != http.StatusOK || !strings.HasPrefix(iconResponse.Header().Get("Content-Type"), "image/png") {
		t.Fatalf("GET /cone.png = %d %q", iconResponse.Code, iconResponse.Header().Get("Content-Type"))
	}
}

func TestWebHandlerStreamsProgressAndJSONResult(t *testing.T) {
	processor := &stubWebProcessor{}
	handler, err := NewWebHandler(processor)
	if err != nil {
		t.Fatal(err)
	}
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("pack", "Fractal 512x.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "test ZIP bytes"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/convert", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("POST /api/convert status = %d: %s", response.Code, response.Body.String())
	}
	if processor.filename != "Fractal 512x.zip" || processor.payload != "test ZIP bytes" {
		t.Fatalf("processor received filename=%q payload=%q", processor.filename, processor.payload)
	}
	lines := strings.Split(strings.TrimSpace(response.Body.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("streamed event count = %d: %s", len(lines), response.Body.String())
	}
	var last struct {
		Type     string         `json:"type"`
		Filename string         `json:"filename"`
		Result   map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatal(err)
	}
	if last.Type != "result" || last.Filename != "Fractal 512x_cone.json" || last.Result["t"] != "buffer" || last.Result["zbase64"] == "" {
		t.Fatalf("unexpected result event: %#v", last)
	}
	encodedResult, err := json.Marshal(last.Result)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeCompressedJSON(encodedResult)
	if err != nil {
		t.Fatal(err)
	}
	if decoded["SwordMesh"] != "123" {
		t.Fatalf("decompressed web result = %#v", decoded)
	}
}

func TestCredentialWebHandlerUsesOnlyRequestCredentials(t *testing.T) {
	processor := &stubWebProcessor{}
	captured := &capturedCredentials{}
	notifier := &stubPortNotifier{}
	handler, err := NewCredentialWebHandlerWithNotifier(func(_ context.Context, apiKey, userID string) (uploadProcessor, error) {
		captured.apiKey = apiKey
		captured.userID = userID
		return processor, nil
	}, notifier)
	if err != nil {
		t.Fatal(err)
	}
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("pack", "public.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "zip"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/convert", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set(robloxAPIKeyHeader, "request-secret")
	request.Header.Set(robloxUserIDHeader, "123456")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("credential conversion status = %d: %s", response.Code, response.Body.String())
	}
	if captured.apiKey != "request-secret" || captured.userID != "123456" {
		t.Fatalf("factory got API key %q and user ID %q", captured.apiKey, captured.userID)
	}
	if notifier.notification.PackID != "0123456789abcdef" || string(notifier.notification.PreviewPNG) != "preview" {
		t.Fatalf("webhook notification = %#v", notifier.notification)
	}
	decoded, err := decodeCompressedJSON(notifier.notification.OutputJSON)
	if err != nil {
		t.Fatal(err)
	}
	if decoded["SwordMesh"] != "123" {
		t.Fatalf("webhook output JSON = %#v", decoded)
	}
}

func TestCredentialWebHandlerRejectsMissingCredentials(t *testing.T) {
	handler, err := NewCredentialWebHandler(func(_ context.Context, _, _ string) (uploadProcessor, error) {
		t.Fatal("processor factory ran without credentials")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/convert", strings.NewReader("unused"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing-credential status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestDownloadFilenameIsSafeAndPortable(t *testing.T) {
	for input, expected := range map[string]string{
		"pack.zip":                   "pack_cone.json",
		`C:\\packs\\orange.zip`:      "orange_cone.json",
		"../../uploaded texture.ZIP": "uploaded texture_cone.json",
	} {
		if actual := downloadFilename(input); actual != expected {
			t.Fatalf("downloadFilename(%q) = %q, want %q", input, actual, expected)
		}
	}
}
