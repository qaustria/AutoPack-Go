package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var embeddedWebFiles embed.FS

type uploadProcessor interface {
	ProcessUpload(context.Context, string, io.Reader, ProgressFunc) (PipelineResult, error)
}

type requestProcessorFactory func(context.Context, string, string) (uploadProcessor, error)

const (
	robloxAPIKeyHeader = "X-Cone-Roblox-Api-Key"
	robloxUserIDHeader = "X-Cone-Roblox-User-Id"
)

type webHandler struct {
	processor        uploadProcessor
	processorFactory requestProcessorFactory
	notifier         PortNotifier
	static           http.Handler
	activeJobsMu     sync.Mutex
	activeJobs       map[[sha256.Size]byte]struct{}
}

type webStreamEvent struct {
	Type     string          `json:"type"`
	Progress *ProgressEvent  `json:"progress,omitempty"`
	Result   *PipelineResult `json:"result,omitempty"`
	Filename string          `json:"filename,omitempty"`
	Message  string          `json:"message,omitempty"`
}

func NewWebHandler(processor uploadProcessor) (http.Handler, error) {
	if processor == nil {
		return nil, errors.New("web upload processor is nil")
	}
	assets, err := fs.Sub(embeddedWebFiles, "web")
	if err != nil {
		return nil, fmt.Errorf("load embedded web files: %w", err)
	}
	handler := newWebHandler(assets)
	handler.processor = processor
	return handler.routes(), nil
}

// NewCredentialWebHandler creates the public-site handler. Each conversion
// gets a fresh processor made from credentials supplied with that request;
// keys are never retained on the handler or written to disk.
func NewCredentialWebHandler(factory requestProcessorFactory) (http.Handler, error) {
	return NewCredentialWebHandlerWithNotifier(factory, nil)
}

func NewCredentialWebHandlerWithNotifier(factory requestProcessorFactory, notifier PortNotifier) (http.Handler, error) {
	if factory == nil {
		return nil, errors.New("web request processor factory is nil")
	}
	assets, err := fs.Sub(embeddedWebFiles, "web")
	if err != nil {
		return nil, fmt.Errorf("load embedded web files: %w", err)
	}
	handler := newWebHandler(assets)
	handler.processorFactory = factory
	handler.notifier = notifier
	return handler.routes(), nil
}

func newWebHandler(assets fs.FS) *webHandler {
	return &webHandler{
		static:     http.FileServer(http.FS(assets)),
		activeJobs: make(map[[sha256.Size]byte]struct{}),
	}
}

func (h *webHandler) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/convert", h.convert)
	mux.HandleFunc("/healthz", h.health)
	mux.Handle("/", h.static)
	return securityHeaders(mux)
}

func (h *webHandler) health(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(response, "{\"status\":\"ok\"}\n")
}

func (h *webHandler) convert(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	processor := h.processor
	jobKey := sha256.Sum256([]byte("cone-shared-web-processor"))
	var apiKey, userID string
	if h.processorFactory != nil {
		apiKey = strings.TrimSpace(request.Header.Get(robloxAPIKeyHeader))
		userID = strings.TrimSpace(request.Header.Get(robloxUserIDHeader))
		if apiKey == "" || userID == "" {
			http.Error(response, "Roblox API key and user ID are required", http.StatusBadRequest)
			return
		}
		if len(apiKey) > 8192 || len(userID) > 32 {
			http.Error(response, "Roblox credentials are too long", http.StatusBadRequest)
			return
		}
		jobKey = sha256.Sum256([]byte(apiKey))
	}
	if !h.reserveJob(jobKey) {
		http.Error(response, "another texture pack is already being processed with this Roblox API key", http.StatusTooManyRequests)
		return
	}
	defer h.releaseJob(jobKey)
	if h.processorFactory != nil {
		var err error
		processor, err = h.processorFactory(request.Context(), apiKey, userID)
		if err != nil {
			http.Error(response, err.Error(), http.StatusUnauthorized)
			return
		}
	}
	request.Body = http.MaxBytesReader(response, request.Body, DefaultMaxTexturePackUploadBytes+(8<<20))
	part, err := texturePackPart(request)
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	defer part.Close()
	filename := filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
	if filename == "." || filename == "" {
		filename = "texture-pack.zip"
	}

	response.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Accel-Buffering", "no")
	encoder := json.NewEncoder(response)
	flusher, _ := response.(http.Flusher)
	writeEvent := func(event webStreamEvent) {
		if err := encoder.Encode(event); err == nil && flusher != nil {
			flusher.Flush()
		}
	}
	result, err := processor.ProcessUpload(request.Context(), filename, part, func(progress ProgressEvent) {
		writeEvent(webStreamEvent{Type: "progress", Progress: &progress})
	})
	if err != nil {
		writeEvent(webStreamEvent{Type: "error", Message: err.Error()})
		return
	}
	outputJSON, err := json.Marshal(result)
	if err != nil {
		writeEvent(webStreamEvent{Type: "error", Message: err.Error()})
		return
	}
	notificationEvent := ProgressEvent{Stage: ProgressNotifying}
	if h.notifier == nil {
		notificationEvent.Message = "Discord webhook not configured; skipped notification"
	} else {
		notifyContext, cancel := context.WithTimeout(request.Context(), 20*time.Second)
		notifyErr := h.notifier.Notify(notifyContext, PortNotification{
			PackID: result.PackID, PackName: result.PackName,
			OutputJSON: outputJSON, PreviewPNG: result.PreviewPNG,
		})
		cancel()
		if notifyErr != nil {
			notificationEvent.Message = "Discord notification failed"
			notificationEvent.Error = notifyErr.Error()
		} else {
			notificationEvent.Message = "Sent Discord notification"
		}
	}
	writeEvent(webStreamEvent{Type: "progress", Progress: &notificationEvent})
	writeEvent(webStreamEvent{
		Type: "result", Result: &result, Filename: downloadFilename(filename),
	})
}

// reserveJob rate-limits conversions per Roblox credential rather than per
// Cone server. Only the SHA-256 digest is retained for the duration of a job;
// the API key itself is never stored on the handler.
func (h *webHandler) reserveJob(key [sha256.Size]byte) bool {
	h.activeJobsMu.Lock()
	defer h.activeJobsMu.Unlock()
	if _, active := h.activeJobs[key]; active {
		return false
	}
	h.activeJobs[key] = struct{}{}
	return true
}

func (h *webHandler) releaseJob(key [sha256.Size]byte) {
	h.activeJobsMu.Lock()
	delete(h.activeJobs, key)
	h.activeJobsMu.Unlock()
}

func texturePackPart(request *http.Request) (*multipart.Part, error) {
	reader, err := request.MultipartReader()
	if err != nil {
		return nil, errors.New("request must be multipart form data")
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("missing texture-pack ZIP field named pack")
		}
		if err != nil {
			return nil, fmt.Errorf("read multipart upload: %w", err)
		}
		if part.FormName() == "pack" && part.FileName() != "" {
			return part, nil
		}
		_ = part.Close()
	}
}

func downloadFilename(uploadName string) string {
	name := filepath.Base(strings.ReplaceAll(uploadName, "\\", "/"))
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if strings.TrimSpace(stem) == "" {
		stem = "texture-pack"
	}
	return stem + "_cone.json"
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}
