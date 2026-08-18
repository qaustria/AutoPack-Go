package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qaustria/AutoPack-Go/packstore"
	"github.com/qaustria/AutoPack-Go/utils"
)

//go:embed web/*
var embeddedWebFiles embed.FS

type uploadProcessor interface {
	ProcessUpload(context.Context, string, io.Reader, ProgressFunc) (PipelineResult, error)
}

type requestProcessorFactory func(context.Context, string, string) (uploadProcessor, error)

const (
	robloxAPIKeyHeader        = "X-Cone-Roblox-Api-Key"
	robloxUserIDHeader        = "X-Cone-Roblox-User-Id"
	batchIndexHeader          = "X-Cone-Batch-Index"
	batchTotalHeader          = "X-Cone-Batch-Total"
	batchTokenHeader          = "X-Cone-Batch-Token"
	defaultMaxConcurrentPorts = 2
)

var errBatchAuthorization = errors.New("administrative batch credentials are invalid")

type WebHandlerOptions struct {
	Notifier           PortNotifier
	Store              *packstore.Store
	MaxConcurrentPorts int
	BatchToken         string
}

type webHandler struct {
	processor        uploadProcessor
	processorFactory requestProcessorFactory
	notifier         PortNotifier
	packStore        *packstore.Store
	static           http.Handler
	activeJobsMu     sync.Mutex
	activeJobs       map[[sha256.Size]byte]struct{}
	jobSlots         chan struct{}
	batchToken       string
}

type webStreamEvent struct {
	Type     string          `json:"type"`
	Progress *ProgressEvent  `json:"progress,omitempty"`
	Result   *PipelineResult `json:"result,omitempty"`
	Filename string          `json:"filename,omitempty"`
	PackID   string          `json:"packId,omitempty"`
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
	handler := newWebHandler(assets, defaultMaxConcurrentPorts, "")
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
	return NewCredentialWebHandlerWithServices(factory, notifier, nil)
}

// NewCredentialWebHandlerWithServices creates a public handler with optional
// Discord notification and durable successful-port history.
func NewCredentialWebHandlerWithServices(factory requestProcessorFactory, notifier PortNotifier, store *packstore.Store) (http.Handler, error) {
	return NewCredentialWebHandlerWithOptions(factory, WebHandlerOptions{
		Notifier: notifier, Store: store, MaxConcurrentPorts: defaultMaxConcurrentPorts,
	})
}

// NewCredentialWebHandlerWithOptions creates a public handler with explicit
// service dependencies and a process-wide conversion ceiling.
func NewCredentialWebHandlerWithOptions(factory requestProcessorFactory, options WebHandlerOptions) (http.Handler, error) {
	if factory == nil {
		return nil, errors.New("web request processor factory is nil")
	}
	if options.MaxConcurrentPorts < 1 || options.MaxConcurrentPorts > 32 {
		return nil, errors.New("maximum concurrent ports must be between 1 and 32")
	}
	options.BatchToken = strings.TrimSpace(options.BatchToken)
	if options.BatchToken != "" && (len(options.BatchToken) < 32 || len(options.BatchToken) > 256) {
		return nil, errors.New("administrative batch token must be between 32 and 256 characters")
	}
	assets, err := fs.Sub(embeddedWebFiles, "web")
	if err != nil {
		return nil, fmt.Errorf("load embedded web files: %w", err)
	}
	handler := newWebHandler(assets, options.MaxConcurrentPorts, options.BatchToken)
	handler.processorFactory = factory
	handler.notifier = options.Notifier
	handler.packStore = options.Store
	return handler.routes(), nil
}

func newWebHandler(assets fs.FS, maxConcurrentPorts int, batchToken string) *webHandler {
	return &webHandler{
		static:     http.FileServer(http.FS(assets)),
		activeJobs: make(map[[sha256.Size]byte]struct{}),
		jobSlots:   make(chan struct{}, maxConcurrentPorts),
		batchToken: batchToken,
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
	_ = json.NewEncoder(response).Encode(map[string]string{"status": "ok", "version": utils.Version})
}

func (h *webHandler) convert(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	processor := h.processor
	batchIndex, batchTotal, err := batchPositionFromHeaders(request.Header, h.batchToken)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errBatchAuthorization) {
			status = http.StatusForbidden
		}
		http.Error(response, err.Error(), status)
		return
	}
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
	if err := h.reserveJob(jobKey); err != nil {
		http.Error(response, err.Error(), http.StatusTooManyRequests)
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
	if h.packStore != nil {
		// Once Roblox has completed a port, retain its record even if the browser
		// closes before the response stream finishes.
		storeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		record, storeErr := h.packStore.Save(storeContext, packstore.Record{
			PackID: result.PackID, PackName: result.PackName, OutputJSON: outputJSON,
		})
		cancel()
		if storeErr != nil {
			writeEvent(webStreamEvent{Type: "error", Message: "save port history: " + storeErr.Error()})
			return
		}
		writeEvent(webStreamEvent{Type: "progress", Progress: &ProgressEvent{
			Stage: ProgressNotifying, Message: fmt.Sprintf("Saved Pack ID %s as port #%d", result.PackID, record.Sequence),
		}})
	}
	notificationEvent := ProgressEvent{Stage: ProgressNotifying}
	if h.notifier == nil {
		notificationEvent.Message = "Discord webhook not configured; skipped notification"
	} else {
		notifyContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		notifyErr := h.notifier.Notify(notifyContext, PortNotification{
			PackID: result.PackID, PackName: result.PackName,
			OutputJSON: outputJSON, PreviewPNG: result.PreviewPNG,
			BatchIndex: batchIndex, BatchTotal: batchTotal,
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
		Type: "result", Result: &result, Filename: downloadFilename(filename), PackID: result.PackID,
	})
}

func batchPositionFromHeaders(headers http.Header, configuredToken string) (int, int, error) {
	indexValue := strings.TrimSpace(headers.Get(batchIndexHeader))
	totalValue := strings.TrimSpace(headers.Get(batchTotalHeader))
	providedToken := strings.TrimSpace(headers.Get(batchTokenHeader))
	if indexValue == "" && totalValue == "" {
		if providedToken != "" {
			return 0, 0, errors.New("administrative batch token requires batch position headers")
		}
		return 0, 0, nil
	}
	if indexValue == "" || totalValue == "" {
		return 0, 0, errors.New("batch index and total headers must be provided together")
	}
	if configuredToken == "" || providedToken == "" || len(configuredToken) != len(providedToken) ||
		subtle.ConstantTimeCompare([]byte(configuredToken), []byte(providedToken)) != 1 {
		return 0, 0, errBatchAuthorization
	}
	index, indexErr := strconv.Atoi(indexValue)
	total, totalErr := strconv.Atoi(totalValue)
	if indexErr != nil || totalErr != nil || index < 1 || total < index || total > maxBatchQueueEntries {
		return 0, 0, fmt.Errorf("batch position must satisfy 1 <= index <= total <= %d", maxBatchQueueEntries)
	}
	return index, total, nil
}

// reserveJob prevents duplicate work per Roblox credential and caps total
// conversion memory across all credentials. Only the SHA-256 key digest is
// retained for the duration of a job.
func (h *webHandler) reserveJob(key [sha256.Size]byte) error {
	h.activeJobsMu.Lock()
	defer h.activeJobsMu.Unlock()
	if _, active := h.activeJobs[key]; active {
		return errors.New("another texture pack is already being processed with this Roblox API key")
	}
	select {
	case h.jobSlots <- struct{}{}:
	default:
		return errors.New("Cone is at its safe processing limit; try again after a current port finishes")
	}
	h.activeJobs[key] = struct{}{}
	return nil
}

func (h *webHandler) releaseJob(key [sha256.Size]byte) {
	h.activeJobsMu.Lock()
	delete(h.activeJobs, key)
	<-h.jobSlots
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
