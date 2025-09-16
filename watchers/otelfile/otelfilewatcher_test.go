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

	// Test OTLP log record in proper format
	testRecord := otlpLogsData{
		ResourceLogs: []resourceLogs{
			{
				Resource: resource{
					Attributes: []keyValue{
						{
							Key: "host.name",
							Value: anyValue{
								StringValue: "server01",
							},
						},
						{
							Key: "service.name",
							Value: anyValue{
								StringValue: "auth-service",
							},
						},
					},
				},
				ScopeLogs: []scopeLogs{
					{
						Scope: scope{},
						LogRecords: []logRecord{
							{
								TimeUnixNano:         "1693526130123456000",
								ObservedTimeUnixNano: "1693526130123456000",
								SeverityNumber:       9,
								SeverityText:         "INFO",
								Body: anyValue{
									StringValue: "User logged in successfully",
								},
								TraceID: "abc123def456",
								SpanID:  "def456abc123",
								Attributes: []keyValue{
									{
										Key: "user.id",
										Value: anyValue{
											StringValue: "12345",
										},
									},
									{
										Key: "http.method",
										Value: anyValue{
											StringValue: "POST",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	testBytes, err := json.Marshal(testRecord)
	if err != nil {
		t.Fatalf("Failed to marshal test record: %v", err)
	}

	events, stats := watcher.normalizeEvents(testBytes)

	if len(events) != 1 {
		t.Fatalf("Expected 1 event, got %d", len(events))
	}

	// Verify that stats were also returned
	if stats.NumMessages != 1 {
		t.Errorf("Expected 1 message in stats, got %d", stats.NumMessages)
	}

	event := events[0]

	// Add basic checks for the event
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
}

func TestOtelFileWatcher_updateStats(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	watcher, err := NewOtelFileWatcher(context.Background(), "/tmp/test.log", false, logger)
	if err != nil {
		t.Fatalf("Failed to create watcher: %v", err)
	}

	// Create test stats to add
	testStats := common.LogStats{
		NumMessages:      4,
		NumInfoMessages:  1,
		NumErrorMessages: 1,
		NumWarnMessages:  1,
		NumDebugMessages: 1,
	}

	watcher.updateStats(testStats)

	// Get a copy of the stats to check (thread-safe)
	currentStats := watcher.getStatsCopy()

	if currentStats.NumMessages != 4 {
		t.Errorf("Expected 4 total messages, got %d", currentStats.NumMessages)
	}

	if currentStats.NumInfoMessages != 1 {
		t.Errorf("Expected 1 info message, got %d", currentStats.NumInfoMessages)
	}

	if currentStats.NumErrorMessages != 1 {
		t.Errorf("Expected 1 error message, got %d", currentStats.NumErrorMessages)
	}

	if currentStats.NumWarnMessages != 1 {
		t.Errorf("Expected 1 warning message, got %d", currentStats.NumWarnMessages)
	}

	if currentStats.NumDebugMessages != 1 {
		t.Errorf("Expected 1 debug message, got %d", currentStats.NumDebugMessages)
	}

	// Test incremental updates
	additionalStats := common.LogStats{
		NumMessages:      2,
		NumInfoMessages:  1,
		NumErrorMessages: 1,
		NumDNSQueries:    5,
		NumDHCPDiscover:  2,
	}

	watcher.updateStats(additionalStats)
	updatedStats := watcher.getStatsCopy()

	if updatedStats.NumMessages != 6 {
		t.Errorf("Expected 6 total messages after increment, got %d", updatedStats.NumMessages)
	}

	if updatedStats.NumInfoMessages != 2 {
		t.Errorf("Expected 2 info messages after increment, got %d", updatedStats.NumInfoMessages)
	}

	if updatedStats.NumDNSQueries != 5 {
		t.Errorf("Expected 5 DNS queries, got %d", updatedStats.NumDNSQueries)
	}

	if updatedStats.NumDHCPDiscover != 2 {
		t.Errorf("Expected 2 DHCP discover messages, got %d", updatedStats.NumDHCPDiscover)
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
	err = watcher.openFile(false)
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
