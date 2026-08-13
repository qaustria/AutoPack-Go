package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/qaustria/AutoPack-Go/utils"
)

const (
	defaultBatchEndpoint        = "http://127.0.0.1:8080/api/convert"
	defaultBatchStatePath       = "data/batch-queue.json"
	defaultBatchOutputDir       = "batch-output"
	maxBatchSheetBytes    int64 = 10 << 20
)

var batchURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

type batchQueueEntry struct {
	Row    int
	Name   string
	Source string
}

type batchQueueState struct {
	Version   int                            `json:"version"`
	Completed map[string]batchCompletedEntry `json:"completed"`
}

type batchCompletedEntry struct {
	Name       string    `json:"name"`
	PackID     string    `json:"packId,omitempty"`
	OutputPath string    `json:"outputPath"`
	FinishedAt time.Time `json:"finishedAt"`
}

type batchAPIEvent struct {
	Type     string          `json:"type"`
	Progress *ProgressEvent  `json:"progress,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	PackID   string          `json:"packId,omitempty"`
	Message  string          `json:"message,omitempty"`
}

// runBatch queues every pack URL in a CSV or public Google Sheet and submits
// one pack at a time to Cone's existing local web API. The server remains the
// single source of truth for processing, history, and Discord notifications.
func runBatch(ctx context.Context, args []string) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return errors.New("usage: cone batch <public Google Sheet URL or CSV path>")
	}
	credentials, err := loadRobloxCredentials()
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	queue, err := loadBatchQueue(ctx, args[0], client)
	if err != nil {
		return err
	}
	if len(queue) == 0 {
		return errors.New("batch source contains no HTTP texture-pack links")
	}

	statePath := strings.TrimSpace(os.Getenv("CONE_BATCH_STATE_PATH"))
	if statePath == "" {
		statePath = defaultBatchStatePath
	}
	outputDir := strings.TrimSpace(os.Getenv("CONE_BATCH_OUTPUT_DIR"))
	if outputDir == "" {
		outputDir = defaultBatchOutputDir
	}
	endpoint := strings.TrimSpace(os.Getenv("CONE_BATCH_ENDPOINT"))
	if endpoint == "" {
		endpoint = defaultBatchEndpoint
	}
	if err := validateBatchEndpoint(endpoint); err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, 0o750); err != nil {
		return fmt.Errorf("create batch output directory: %w", err)
	}
	state, err := loadBatchState(statePath)
	if err != nil {
		return err
	}

	completed := 0
	failed := 0
	skipped := 0
	fmt.Printf("Cone batch: %d queued packs\n", len(queue))
	for index, entry := range queue {
		if _, done := state.Completed[entry.Source]; done {
			skipped++
			fmt.Printf("[%d/%d] SKIP %s (already completed)\n", index+1, len(queue), entry.Name)
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Printf("[%d/%d] DOWNLOAD %s\n", index+1, len(queue), entry.Name)
		zipPath, filename, err := downloadBatchPack(ctx, client, entry)
		if err != nil {
			failed++
			fmt.Printf("[%d/%d] FAILED %s: %v\n", index+1, len(queue), entry.Name, err)
			continue
		}
		result, err := submitBatchPackWhenAvailable(ctx, client, endpoint, credentials, zipPath, filename, func(progress ProgressEvent) {
			message := progress.Message
			if message == "" {
				message = progress.Name
			}
			if progress.Error != "" {
				if message == "" {
					message = "Skipped asset"
				}
				message += ": " + progress.Error
			}
			if message != "" {
				fmt.Printf("[%d/%d] %s: %s\n", index+1, len(queue), progress.Stage, message)
			}
		})
		_ = os.Remove(zipPath)
		if err != nil {
			failed++
			fmt.Printf("[%d/%d] FAILED %s: %v\n", index+1, len(queue), entry.Name, err)
			continue
		}
		outputPath := filepath.Join(outputDir, batchOutputFilename(filename, result.PackID))
		if err := os.WriteFile(outputPath, append(result.Result, '\n'), 0o640); err != nil {
			return fmt.Errorf("write batch output for %q: %w", entry.Name, err)
		}
		state.Completed[entry.Source] = batchCompletedEntry{
			Name: entry.Name, PackID: result.PackID, OutputPath: outputPath, FinishedAt: time.Now().UTC(),
		}
		if err := saveBatchState(statePath, state); err != nil {
			return err
		}
		completed++
		fmt.Printf("[%d/%d] DONE %s (Pack ID %s)\n", index+1, len(queue), entry.Name, result.PackID)
	}
	fmt.Printf("Cone batch finished: %d completed, %d skipped, %d failed\n", completed, skipped, failed)
	if failed != 0 {
		return fmt.Errorf("%d batch packs failed; rerun the same command to retry only unfinished rows", failed)
	}
	return nil
}

type batchSubmitResult struct {
	PackID string
	Result json.RawMessage
}

type batchServerError struct {
	StatusCode int
	Status     string
	Message    string
	RetryAfter time.Duration
}

func (err *batchServerError) Error() string {
	if err.Message == "" {
		return "Cone returned " + err.Status
	}
	return "Cone returned " + err.Status + ": " + err.Message
}

func submitBatchPackWhenAvailable(ctx context.Context, client *http.Client, endpoint string, credentials robloxCredentials, zipPath, filename string, progress ProgressFunc) (batchSubmitResult, error) {
	deadline := time.Now().Add(30 * time.Minute)
	for {
		result, err := submitBatchPack(ctx, client, endpoint, credentials, zipPath, filename, progress)
		var serverError *batchServerError
		if !errors.As(err, &serverError) || serverError.StatusCode != http.StatusTooManyRequests {
			return result, err
		}
		if time.Now().After(deadline) {
			return batchSubmitResult{}, fmt.Errorf("wait for Cone queue slot: %w", err)
		}
		delay := serverError.RetryAfter
		if delay <= 0 || delay > time.Minute {
			delay = 5 * time.Second
		}
		fmt.Printf("Cone is busy; retrying this pack in %s\n", delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return batchSubmitResult{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func submitBatchPack(ctx context.Context, client *http.Client, endpoint string, credentials robloxCredentials, zipPath, filename string, progress ProgressFunc) (batchSubmitResult, error) {
	file, err := os.Open(zipPath)
	if err != nil {
		return batchSubmitResult{}, fmt.Errorf("open queued pack: %w", err)
	}
	defer file.Close()

	requestContext, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	multipartWriter := multipart.NewWriter(writer)
	writeDone := make(chan error, 1)
	go func() {
		part, createErr := multipartWriter.CreateFormFile("pack", filename)
		if createErr == nil {
			_, createErr = io.Copy(part, file)
		}
		if closeErr := multipartWriter.Close(); createErr == nil {
			createErr = closeErr
		}
		_ = writer.CloseWithError(createErr)
		writeDone <- createErr
	}()

	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, endpoint, reader)
	if err != nil {
		_ = reader.CloseWithError(err)
		return batchSubmitResult{}, fmt.Errorf("create queued port request: %w", err)
	}
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	request.Header.Set(robloxAPIKeyHeader, credentials.APIKey)
	if credentials.UserID == "" {
		return batchSubmitResult{}, errors.New("batch mode currently requires a Roblox user ID")
	}
	request.Header.Set(robloxUserIDHeader, credentials.UserID)
	response, err := client.Do(request)
	if err != nil {
		_ = reader.CloseWithError(err)
		return batchSubmitResult{}, fmt.Errorf("submit queued pack: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		_ = reader.CloseWithError(errors.New("Cone rejected queued pack"))
		return batchSubmitResult{}, &batchServerError{
			StatusCode: response.StatusCode,
			Status:     response.Status,
			Message:    strings.TrimSpace(string(body)),
			RetryAfter: parseBatchRetryAfter(response.Header.Get("Retry-After")),
		}
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var result batchSubmitResult
	for scanner.Scan() {
		var event batchAPIEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			_ = reader.CloseWithError(err)
			return batchSubmitResult{}, fmt.Errorf("decode Cone batch event: %w", err)
		}
		if event.Progress != nil && progress != nil {
			progress(*event.Progress)
		}
		switch event.Type {
		case "error":
			_ = reader.CloseWithError(errors.New(event.Message))
			return batchSubmitResult{}, errors.New(event.Message)
		case "result":
			if !json.Valid(event.Result) {
				return batchSubmitResult{}, errors.New("Cone returned invalid result JSON")
			}
			result = batchSubmitResult{PackID: event.PackID, Result: append(json.RawMessage(nil), event.Result...)}
		}
	}
	if err := scanner.Err(); err != nil {
		_ = reader.CloseWithError(err)
		return batchSubmitResult{}, fmt.Errorf("read Cone batch events: %w", err)
	}
	if err := <-writeDone; err != nil {
		return batchSubmitResult{}, fmt.Errorf("stream queued pack: %w", err)
	}
	if result.PackID == "" || len(result.Result) == 0 {
		return batchSubmitResult{}, errors.New("Cone finished without a batch result")
	}
	return result, nil
}

func parseBatchRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if timestamp, err := http.ParseTime(value); err == nil {
		if delay := time.Until(timestamp); delay > 0 {
			return delay
		}
	}
	return 0
}

func loadBatchQueue(ctx context.Context, source string, client *http.Client) ([]batchQueueEntry, error) {
	reader, closeReader, err := openBatchCSV(ctx, strings.TrimSpace(source), client)
	if err != nil {
		return nil, err
	}
	defer closeReader()
	data, err := io.ReadAll(io.LimitReader(reader, maxBatchSheetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read batch CSV: %w", err)
	}
	if int64(len(data)) > maxBatchSheetBytes {
		return nil, fmt.Errorf("batch CSV exceeds the %d-byte limit", maxBatchSheetBytes)
	}
	records, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read batch CSV: %w", err)
	}
	return parseBatchRecords(records), nil
}

func openBatchCSV(ctx context.Context, source string, client *http.Client) (io.Reader, func(), error) {
	if sheetURL, ok := googleSheetCSVURL(source); ok {
		response, err := batchGET(ctx, client, sheetURL)
		if err != nil {
			if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") {
				return nil, func() {}, fmt.Errorf("read Google Sheet: %w; share it as Anyone with the link → Viewer", err)
			}
			return nil, func() {}, fmt.Errorf("read Google Sheet: %w", err)
		}
		return io.LimitReader(response.Body, maxBatchSheetBytes+1), func() { _ = response.Body.Close() }, nil
	}
	if parsed, err := url.Parse(source); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		response, err := batchGET(ctx, client, source)
		if err != nil {
			return nil, func() {}, fmt.Errorf("download batch CSV: %w", err)
		}
		return io.LimitReader(response.Body, maxBatchSheetBytes+1), func() { _ = response.Body.Close() }, nil
	}
	file, err := os.Open(source)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open batch CSV %q: %w", source, err)
	}
	return io.LimitReader(file, maxBatchSheetBytes+1), func() { _ = file.Close() }, nil
}

func googleSheetCSVURL(source string) (string, bool) {
	parsed, err := url.Parse(source)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "docs.google.com") {
		return "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "spreadsheets" || parts[1] != "d" || parts[2] == "" {
		return "", false
	}
	gid := parsed.Query().Get("gid")
	if gid == "" {
		if fragment, err := url.ParseQuery(parsed.Fragment); err == nil {
			gid = fragment.Get("gid")
		}
	}
	if gid == "" {
		gid = "0"
	}
	return "https://docs.google.com/spreadsheets/d/" + url.PathEscape(parts[2]) + "/export?format=csv&gid=" + url.QueryEscape(gid), true
}

func parseBatchRecords(records [][]string) []batchQueueEntry {
	if len(records) == 0 {
		return nil
	}
	nameColumn := -1
	for column, value := range records[0] {
		header := strings.ToLower(strings.TrimSpace(value))
		if header == "name" || header == "pack name" || header == "texture pack" || header == "texturepack" {
			nameColumn = column
			break
		}
	}
	seen := make(map[string]struct{})
	var queue []batchQueueEntry
	for rowIndex, row := range records {
		name := ""
		if nameColumn >= 0 && nameColumn < len(row) {
			name = strings.TrimSpace(row[nameColumn])
		}
		for _, cell := range row {
			for _, match := range batchURLPattern.FindAllString(cell, -1) {
				match = strings.TrimRight(html.UnescapeString(match), ").,;]}")
				parsed, err := url.Parse(match)
				if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
					continue
				}
				parsed.Fragment = ""
				match = parsed.String()
				if _, exists := seen[match]; exists {
					continue
				}
				seen[match] = struct{}{}
				entryName := name
				if entryName == "" {
					entryName = batchNameFromURL(parsed, rowIndex+1)
				}
				queue = append(queue, batchQueueEntry{Row: rowIndex + 1, Name: entryName, Source: match})
			}
		}
	}
	return queue
}

func downloadBatchPack(ctx context.Context, client *http.Client, entry batchQueueEntry) (string, string, error) {
	source := normalizeBatchDownloadURL(entry.Source)
	response, err := batchGET(ctx, client, source)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	filename := batchDownloadFilename(response, entry)
	file, err := os.CreateTemp("", "cone-batch-*.zip")
	if err != nil {
		return "", "", fmt.Errorf("create queued ZIP: %w", err)
	}
	path := file.Name()
	reader := &io.LimitedReader{R: response.Body, N: DefaultMaxTexturePackUploadBytes + 1}
	written, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		return "", "", fmt.Errorf("download queued ZIP: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", "", fmt.Errorf("close queued ZIP: %w", closeErr)
	}
	if written == 0 || written > DefaultMaxTexturePackUploadBytes {
		_ = os.Remove(path)
		return "", "", fmt.Errorf("queued ZIP size %d is outside the 1-%d byte limit", written, DefaultMaxTexturePackUploadBytes)
	}
	archive, err := os.Open(path)
	if err != nil {
		_ = os.Remove(path)
		return "", "", err
	}
	var signature [4]byte
	_, signatureErr := io.ReadFull(archive, signature[:])
	_ = archive.Close()
	if signatureErr != nil || (string(signature[:]) != "PK\x03\x04" && string(signature[:]) != "PK\x05\x06" && string(signature[:]) != "PK\x07\x08") {
		_ = os.Remove(path)
		return "", "", errors.New("downloaded file is not a ZIP; the sheet may contain a landing-page link")
	}
	return path, filename, nil
}

func normalizeBatchDownloadURL(source string) string {
	parsed, err := url.Parse(source)
	if err != nil {
		return source
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "www.dropbox.com" || host == "dropbox.com" {
		query := parsed.Query()
		query.Set("dl", "1")
		parsed.RawQuery = query.Encode()
	}
	if host == "drive.google.com" {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		fileID := parsed.Query().Get("id")
		if len(parts) >= 3 && parts[0] == "file" && parts[1] == "d" {
			fileID = parts[2]
		}
		if fileID != "" {
			return "https://drive.usercontent.google.com/download?id=" + url.QueryEscape(fileID) + "&export=download&confirm=t"
		}
	}
	return parsed.String()
}

func batchGET(ctx context.Context, client *http.Client, target string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Cone/"+utils.Version+" batch porter")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = response.Body.Close()
		return nil, fmt.Errorf("%s returned %s", request.URL.Hostname(), response.Status)
	}
	return response, nil
}

func validateBatchEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("CONE_BATCH_ENDPOINT must be an HTTP or HTTPS URL")
	}
	return nil
}

func loadBatchState(path string) (batchQueueState, error) {
	state := batchQueueState{Version: 1, Completed: make(map[string]batchCompletedEntry)}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read batch state: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("decode batch state: %w", err)
	}
	if state.Completed == nil {
		state.Completed = make(map[string]batchCompletedEntry)
	}
	return state, nil
}

func saveBatchState(path string, state batchQueueState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create batch state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode batch state: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".cone-batch-state-*")
	if err != nil {
		return fmt.Errorf("create batch state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write batch state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close batch state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace batch state: %w", err)
	}
	return nil
}

func batchNameFromURL(parsed *url.URL, row int) string {
	name := filepath.Base(strings.TrimSuffix(parsed.Path, "/"))
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	if name == "" || name == "." || name == "/" {
		name = fmt.Sprintf("pack-%03d.zip", row)
	}
	return name
}

func batchDownloadFilename(response *http.Response, entry batchQueueEntry) string {
	filename := ""
	if disposition := response.Header.Get("Content-Disposition"); disposition != "" {
		if _, parameters, err := mime.ParseMediaType(disposition); err == nil {
			filename = parameters["filename"]
		}
	}
	if filename == "" {
		filename = entry.Name
	}
	filename = filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	if !strings.EqualFold(filepath.Ext(filename), ".zip") {
		filename += ".zip"
	}
	return filename
}

func batchOutputFilename(filename, packID string) string {
	stem := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	stem = strings.Map(func(character rune) rune {
		if character == ' ' || character == '-' || character == '_' ||
			character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			return character
		}
		return '_'
	}, stem)
	stem = strings.Trim(strings.Join(strings.Fields(stem), "_"), "._-")
	if stem == "" {
		stem = "pack"
	}
	if packID == "" {
		hash := sha256.Sum256([]byte(filename))
		packID = hex.EncodeToString(hash[:8])
	}
	return stem + "_" + packID + "_cone.json"
}
