package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessUploadProducesJSONReadyResultAndProgress(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "pack.zip")
	writeCompletePack(t, zipPath)
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	uploader := &fakeUploader{}
	processor, err := NewProcessor(ProcessorConfig{Uploader: uploader})
	if err != nil {
		t.Fatal(err)
	}
	var events []ProgressEvent
	result, err := processor.ProcessUpload(context.Background(), "browser-upload.ZIP", bytes.NewReader(data), func(event ProgressEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 4 || events[0].Stage != ProgressReceiving || events[1].Stage != ProgressPreparing || events[len(events)-1].Stage != ProgressComplete {
		t.Fatalf("unexpected progress lifecycle: %#v", events)
	}
	var logText strings.Builder
	for _, event := range events {
		logText.WriteString(event.Message)
		logText.WriteByte('\n')
	}
	for _, expected := range []string{"Received browser-upload.ZIP", "Found ", "Resized ", "greedy meshes", "Uploaded Cone ", "Generated JSON mapping"} {
		if !strings.Contains(logText.String(), expected) {
			t.Fatalf("progress log does not contain %q:\n%s", expected, logText.String())
		}
	}
	if result.Values["SwordMesh"] == "" || result.Values["GoldAppleRotation"] == nil {
		t.Fatalf("incomplete processor result: %#v", result.Values)
	}
	digest := sha256.Sum256(data)
	expectedID := fmt.Sprintf("%x", digest[:8])
	if result.PackID != expectedID || result.PackName != "browser-upload.ZIP" {
		t.Fatalf("pack identity = %q %q, want %q browser-upload.ZIP", result.PackID, result.PackName, expectedID)
	}
	if len(result.PreviewPNG) == 0 {
		t.Fatal("processor result is missing its hotbar preview")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"Values"`)) || !bytes.Contains(encoded, []byte(`"t":"buffer"`)) || !bytes.Contains(encoded, []byte(`"zbase64"`)) {
		t.Fatalf("result is not a compressed buffer envelope: %s", encoded)
	}
	decoded, err := decodeCompressedJSON(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded["SwordMesh"] == "" {
		t.Fatalf("compressed result is missing SwordMesh: %#v", decoded)
	}
}

func TestProcessUploadEnforcesCompressedSizeLimit(t *testing.T) {
	processor, err := NewProcessor(ProcessorConfig{Uploader: &fakeUploader{}, MaxUploadBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	var events []ProgressEvent
	_, err = processor.ProcessUpload(context.Background(), "pack.zip", strings.NewReader("12345"), func(event ProgressEvent) {
		events = append(events, event)
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds 4-byte limit") {
		t.Fatalf("size-limit error = %v", err)
	}
	if len(events) != 1 || events[0].Stage != ProgressFailed {
		t.Fatalf("size-limit progress = %#v", events)
	}
}

func TestProcessUploadHonorsCancellationBeforeStaging(t *testing.T) {
	uploader := &fakeUploader{}
	processor, err := NewProcessor(ProcessorConfig{Uploader: uploader})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = processor.ProcessUpload(ctx, "pack.zip", strings.NewReader("unused"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled upload error = %v", err)
	}
	if len(uploader.requests) != 0 {
		t.Fatal("canceled upload reached uploader")
	}
}

func TestNewProcessorValidatesConfiguration(t *testing.T) {
	if _, err := NewProcessor(ProcessorConfig{}); err == nil {
		t.Fatal("nil uploader was accepted")
	}
	if _, err := NewProcessor(ProcessorConfig{Uploader: &fakeUploader{}, MaxUploadBytes: -1}); err == nil {
		t.Fatal("negative upload limit was accepted")
	}
}
