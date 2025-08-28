package otelfile

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/DRuggeri/labwatch/watchers/common"
)

func TestOtelFileWatcher_normalizeEvents(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	watcher, err := NewOtelFileWatcher(context.Background(), "/tmp/test.log", true, logger)
	if err != nil {
		t.Fatalf("Failed to create watcher: %v", err)
	}

	// Test OTEL log record
	testRecord := otelLogRecord{
		Timestamp:         "2025-08-28T10:15:30.123456Z",
		ObservedTimestamp: "2025-08-28T10:15:30.123456Z",
		TraceID:           "abc123def456",
		SpanID:            "def456abc123",
		SeverityText:      "INFO",
		SeverityNumber:    9,
		Body:              "User logged in successfully",
		Attributes: map[string]interface{}{
			"user.id":     "12345",
			"http.method": "POST",
			"host.name":   "server01",
		},
		Resource: map[string]interface{}{
			"service.name":    "auth-service",
			"service.version": "1.0.0",
			"host.name":       "server01",
		},
	}

	testBytes, err := json.Marshal(testRecord)
	if err != nil {
		t.Fatalf("Failed to marshal test record: %v", err)
	}

	events := watcher.normalizeEvents(testBytes)

	if len(events) != 1 {
		t.Fatalf("Expected 1 event, got %d", len(events))
	}

	event := events[0]

	if event.Node != "server01" {
		t.Errorf("Expected node 'server01', got '%s'", event.Node)
	}

	if event.Service != "auth-service" {
		t.Errorf("Expected service 'auth-service', got '%s'", event.Service)
	}

	if event.Level != "info" {
		t.Errorf("Expected level 'info', got '%s'", event.Level)
	}

	if event.Message != "User logged in successfully" {
		t.Errorf("Expected message 'User logged in successfully', got '%s'", event.Message)
	}

	// Check that attributes are preserved
	if event.Attributes["user.id"] != "12345" {
		t.Errorf("Expected user.id '12345', got '%s'", event.Attributes["user.id"])
	}

	if event.Attributes["trace_id"] != "abc123def456" {
		t.Errorf("Expected trace_id 'abc123def456', got '%s'", event.Attributes["trace_id"])
	}

	if event.Attributes["resource.service.name"] != "auth-service" {
		t.Errorf("Expected resource.service.name 'auth-service', got '%s'", event.Attributes["resource.service.name"])
	}
}

func TestOtelFileWatcher_updateStats(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	watcher, err := NewOtelFileWatcher(context.Background(), "/tmp/test.log", false, logger)
	if err != nil {
		t.Fatalf("Failed to create watcher: %v", err)
	}

	events := []common.LogEvent{
		{Level: "info", Message: "Test info message"},
		{Level: "error", Message: "Test error message"},
		{Level: "warning", Message: "Test warning message"},
		{Level: "debug", Message: "Test debug message"},
	}

	watcher.updateStats(events)

	if watcher.stats.NumMessages != 4 {
		t.Errorf("Expected 4 total messages, got %d", watcher.stats.NumMessages)
	}

	if watcher.stats.NumInfoMessages != 1 {
		t.Errorf("Expected 1 info message, got %d", watcher.stats.NumInfoMessages)
	}

	if watcher.stats.NumErrorMessages != 1 {
		t.Errorf("Expected 1 error message, got %d", watcher.stats.NumErrorMessages)
	}

	if watcher.stats.NumWarnMessages != 1 {
		t.Errorf("Expected 1 warning message, got %d", watcher.stats.NumWarnMessages)
	}

	if watcher.stats.NumDebugMessages != 1 {
		t.Errorf("Expected 1 debug message, got %d", watcher.stats.NumDebugMessages)
	}
}

func TestOtelFileWatcher_fileRotationDetection(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// Create a temporary file
	tmpFile, err := os.CreateTemp("", "test_otel_*.log")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write some initial content
	tmpFile.WriteString(`{"body":"initial message","severity_text":"INFO"}` + "\n")
	tmpFile.Close()

	watcher, err := NewOtelFileWatcher(context.Background(), tmpFile.Name(), false, logger)
	if err != nil {
		t.Fatalf("Failed to create watcher: %v", err)
	}

	// Open the file
	err = watcher.openFile()
	if err != nil {
		t.Fatalf("Failed to open file: %v", err)
	}

	originalSize := watcher.lastFileInfo.Size()

	// Simulate rotation by creating a new smaller file
	newTmpFile, err := os.Create(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to recreate temp file: %v", err)
	}
	newTmpFile.WriteString(`{"body":"new message","severity_text":"INFO"}` + "\n")
	newTmpFile.Close()

	// Check if rotation is detected
	time.Sleep(10 * time.Millisecond) // Small delay to ensure different file stats
	rotated := watcher.checkFileRotation()

	if !rotated {
		t.Errorf("Expected file rotation to be detected")
	}

	t.Logf("Original size: %d, new size should be smaller", originalSize)

	watcher.currentFile.Close()
}
