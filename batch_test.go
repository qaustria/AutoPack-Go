package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGoogleSheetCSVURL(t *testing.T) {
	got, ok := googleSheetCSVURL("https://docs.google.com/spreadsheets/d/sheet-id/edit?usp=sharing#gid=123")
	if !ok {
		t.Fatal("Google Sheet URL was not recognized")
	}
	want := "https://docs.google.com/spreadsheets/d/sheet-id/export?format=csv&gid=123"
	if got != want {
		t.Fatalf("CSV URL = %q, want %q", got, want)
	}
}

func TestParseBatchRecordsUsesNamesAndDeduplicatesLinks(t *testing.T) {
	records := [][]string{
		{"Pack Name", "Download"},
		{"First Pack", "https://example.com/first.zip"},
		{"Duplicate", "https://example.com/first.zip"},
		{"Second Pack", "Get it at https://example.com/second.zip."},
	}
	queue := parseBatchRecords(records)
	if len(queue) != 2 {
		t.Fatalf("queue length = %d, want 2: %#v", len(queue), queue)
	}
	if queue[0].Name != "First Pack" || queue[0].Row != 2 || queue[0].Source != "https://example.com/first.zip" {
		t.Fatalf("first queue entry = %#v", queue[0])
	}
	if queue[1].Name != "Second Pack" || queue[1].Source != "https://example.com/second.zip" {
		t.Fatalf("second queue entry = %#v", queue[1])
	}
}

func TestRunBatchQueuesSequentiallyAndResumes(t *testing.T) {
	zipBytes := testBatchZIP(t)
	var baseURL string
	var requests atomic.Int32
	var active atomic.Int32
	var maximumActive atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/packs.csv":
			response.Header().Set("Content-Type", "text/csv")
			_, _ = fmt.Fprintf(response, "Pack Name,Download\nOne,%s/one.zip\nTwo,%s/two.zip\n", baseURL, baseURL)
		case "/one.zip", "/two.zip":
			response.Header().Set("Content-Type", "application/zip")
			response.Header().Set("Content-Disposition", `attachment; filename="`+strings.TrimPrefix(request.URL.Path, "/")+`"`)
			_, _ = response.Write(zipBytes)
		case "/api/convert":
			if request.Header.Get(robloxAPIKeyHeader) != "test-api-key" || request.Header.Get(robloxUserIDHeader) != "12345" {
				http.Error(response, "bad credentials", http.StatusUnauthorized)
				return
			}
			current := active.Add(1)
			defer active.Add(-1)
			for {
				previous := maximumActive.Load()
				if current <= previous || maximumActive.CompareAndSwap(previous, current) {
					break
				}
			}
			part, err := texturePackPart(request)
			if err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			if _, err := io.Copy(io.Discard, part); err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			requestNumber := requests.Add(1)
			response.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = fmt.Fprintf(response, "{\"type\":\"progress\",\"progress\":{\"message\":\"Porting %d\"}}\n", requestNumber)
			_, _ = fmt.Fprintf(response, "{\"type\":\"result\",\"packId\":\"pack-%d\",\"result\":{\"m\":null,\"t\":\"buffer\",\"zbase64\":\"data-%d\"}}\n", requestNumber, requestNumber)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	baseURL = server.URL

	t.Setenv("ROBLOX_API_KEY", "test-api-key")
	t.Setenv("ROBLOX_USER_ID", "12345")
	t.Setenv("ROBLOX_GROUP_ID", "")
	t.Setenv("CONE_BATCH_ENDPOINT", server.URL+"/api/convert")
	temporary := t.TempDir()
	statePath := filepath.Join(temporary, "state", "queue.json")
	outputDir := filepath.Join(temporary, "output")
	t.Setenv("CONE_BATCH_STATE_PATH", statePath)
	t.Setenv("CONE_BATCH_OUTPUT_DIR", outputDir)

	if err := runBatch(context.Background(), []string{server.URL + "/packs.csv"}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || maximumActive.Load() != 1 {
		t.Fatalf("port requests = %d, maximum active = %d; want 2 and 1", requests.Load(), maximumActive.Load())
	}
	state, err := loadBatchState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Completed) != 2 {
		t.Fatalf("completed state entries = %d, want 2", len(state.Completed))
	}
	outputs, err := filepath.Glob(filepath.Join(outputDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 {
		t.Fatalf("output files = %d, want 2", len(outputs))
	}
	for _, path := range outputs {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(data) || !bytes.Contains(data, []byte(`"zbase64"`)) {
			t.Fatalf("invalid output %s: %s", path, data)
		}
	}

	if err := runBatch(context.Background(), []string{server.URL + "/packs.csv"}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("resume submitted completed packs; requests = %d, want 2", requests.Load())
	}
}

func testBatchZIP(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("assets/minecraft/textures/items/diamond_sword.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("test")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
