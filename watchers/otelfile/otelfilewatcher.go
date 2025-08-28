package otelfile

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/DRuggeri/labwatch/watchers/common"
)

var sleepDuration = time.Duration(250) * time.Millisecond
var fileCheckDuration = time.Duration(1) * time.Second

type OtelFileWatcherConfig struct {
	Path string `yaml:"path"`
}

type OtelFileWatcher struct {
	path             string
	trace            bool
	internalLogChan  chan common.LogEvent
	internalStatChan chan common.LogStats
	stats            common.LogStats
	log              *slog.Logger
	currentFile      *os.File
	scanner          *bufio.Scanner
	lastFileInfo     os.FileInfo
}

func NewOtelFileWatcher(ctx context.Context, path string, trace bool, log *slog.Logger) (*OtelFileWatcher, error) {
	if log == nil {
		log = slog.Default()
	}

	return &OtelFileWatcher{
		path:             path,
		trace:            trace,
		internalLogChan:  make(chan common.LogEvent),
		internalStatChan: make(chan common.LogStats),
		log:              log.With("operation", "OtelFileWatcher"),
	}, nil
}

func (w *OtelFileWatcher) openFile() error {
	if w.currentFile != nil {
		w.currentFile.Close()
		w.currentFile = nil
		w.scanner = nil
	}

	file, err := os.Open(w.path)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", w.path, err)
	}

	// Get file info for rotation detection
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("failed to stat file %s: %w", w.path, err)
	}

	w.currentFile = file
	w.lastFileInfo = info
	w.scanner = bufio.NewScanner(file)

	// Seek to end of file to start tailing
	w.currentFile.Seek(0, 2)

	w.log.Info("opened file for tailing", "path", w.path, "size", info.Size())
	return nil
}

func (w *OtelFileWatcher) checkFileRotation() bool {
	// Check if the file path still points to the same file
	info, err := os.Stat(w.path)
	if err != nil {
		w.log.Warn("failed to stat file for rotation check", "path", w.path, "error", err)
		return true // Assume rotation if we can't stat
	}

	// Compare file identity (different ways on different systems)
	if w.lastFileInfo == nil {
		return true
	}

	// Check if size decreased (strong indicator of rotation)
	if info.Size() < w.lastFileInfo.Size() {
		w.log.Info("file rotation detected - size decreased",
			"path", w.path,
			"oldSize", w.lastFileInfo.Size(),
			"newSize", info.Size())
		return true
	}

	// Check if modification time is significantly different
	if info.ModTime().Before(w.lastFileInfo.ModTime()) {
		w.log.Info("file rotation detected - modification time went backwards",
			"path", w.path,
			"oldModTime", w.lastFileInfo.ModTime(),
			"newModTime", info.ModTime())
		return true
	}

	return false
}

func (w *OtelFileWatcher) Watch(controlContext context.Context, eventChan chan<- common.LogEvent, statChan chan<- common.LogStats) {
	// File reading goroutine
	go func() {
		lastFileCheck := time.Now()

		for {
			select {
			case <-controlContext.Done():
				return
			default:
				// Not ready to read from control channel - carry on
			}

			// Check if we need to open/reopen the file
			if w.currentFile == nil || time.Since(lastFileCheck) > fileCheckDuration {
				if w.currentFile != nil && w.checkFileRotation() {
					w.log.Info("reopening file due to rotation")
					w.openFile()
				} else if w.currentFile == nil {
					if err := w.openFile(); err != nil {
						w.log.Error("failed to open file", "error", err)
						time.Sleep(sleepDuration)
						continue
					}
				}
				lastFileCheck = time.Now()
			}

			// Try to read a line
			if w.scanner.Scan() {
				line := w.scanner.Text()
				if line != "" {
					w.log.Debug("read line", "length", len(line))
					events := w.normalizeEvents([]byte(line))
					w.log.Debug("normalized events", "count", len(events))

					if len(events) > 0 {
						for _, e := range events {
							w.internalLogChan <- e
						}
						w.updateStats(events)
						w.internalStatChan <- w.stats
					}
				}
			} else {
				// No new lines available, sleep briefly
				time.Sleep(sleepDuration)
			}

			// Check for scanner errors
			if err := w.scanner.Err(); err != nil {
				w.log.Error("scanner error", "error", err)
				// Force file reopen on next iteration
				if w.currentFile != nil {
					w.currentFile.Close()
					w.currentFile = nil
					w.scanner = nil
				}
				time.Sleep(sleepDuration)
			}
		}
	}()

	// Event forwarding loop
	for {
		select {
		case <-controlContext.Done():
			if w.currentFile != nil {
				w.currentFile.Close()
			}
			return
		default:
			// Not ready to read from control channel - carry on
		}

		// See if there are any messages to read from the internal channels
	OUTER:
		for {
			select {
			case event := <-w.internalLogChan:
				eventChan <- event
			case stats := <-w.internalStatChan:
				statChan <- stats
			default:
				break OUTER
			}
		}

		time.Sleep(sleepDuration)
	}
}

/*
OpenTelemetry log format (JSON lines):
{
  "timestamp": "2025-08-28T10:15:30.123456Z",
  "observed_timestamp": "2025-08-28T10:15:30.123456Z",
  "trace_id": "abc123def456",
  "span_id": "def456abc123",
  "severity_text": "INFO",
  "severity_number": 9,
  "body": "User logged in successfully",
  "attributes": {
    "service.name": "auth-service",
    "host.name": "server01",
    "user.id": "12345",
    "http.method": "POST"
  },
  "resource": {
    "service.name": "auth-service",
    "service.version": "1.0.0",
    "host.name": "server01"
  }
}
*/

type otelLogRecord struct {
	Timestamp         string                 `json:"timestamp"`
	ObservedTimestamp string                 `json:"observed_timestamp"`
	TraceID           string                 `json:"trace_id"`
	SpanID            string                 `json:"span_id"`
	SeverityText      string                 `json:"severity_text"`
	SeverityNumber    int                    `json:"severity_number"`
	Body              string                 `json:"body"`
	Attributes        map[string]interface{} `json:"attributes"`
	Resource          map[string]interface{} `json:"resource"`
}

func (w *OtelFileWatcher) normalizeEvents(m []byte) []common.LogEvent {
	ret := []common.LogEvent{}

	// Parse single JSON line
	record := otelLogRecord{}
	err := json.Unmarshal(m, &record)
	if err != nil {
		w.log.Error("error unmarshalling OTEL log record", "error", err, "received", string(m))
		return ret
	}

	// Extract node and service information
	node := ""
	service := ""

	// Try to get host name from resource first, then attributes
	if record.Resource != nil {
		if hostName, ok := record.Resource["host.name"].(string); ok {
			node = hostName
		}
		if serviceName, ok := record.Resource["service.name"].(string); ok {
			service = serviceName
		}
	}

	// Fallback to attributes if not found in resource
	if node == "" && record.Attributes != nil {
		if hostName, ok := record.Attributes["host.name"].(string); ok {
			node = hostName
		}
	}
	if service == "" && record.Attributes != nil {
		if serviceName, ok := record.Attributes["service.name"].(string); ok {
			service = serviceName
		}
	}

	// Convert attributes to string map
	stringAttrs := make(map[string]string)
	if record.Attributes != nil {
		for k, v := range record.Attributes {
			stringAttrs[k] = fmt.Sprintf("%v", v)
		}
	}
	if record.Resource != nil {
		for k, v := range record.Resource {
			stringAttrs["resource."+k] = fmt.Sprintf("%v", v)
		}
	}

	// Add OTEL-specific fields to attributes
	if record.TraceID != "" {
		stringAttrs["trace_id"] = record.TraceID
	}
	if record.SpanID != "" {
		stringAttrs["span_id"] = record.SpanID
	}
	if record.Timestamp != "" {
		stringAttrs["timestamp"] = record.Timestamp
	}
	if record.ObservedTimestamp != "" {
		stringAttrs["observed_timestamp"] = record.ObservedTimestamp
	}

	e := common.LogEvent{
		Node:       node,
		Service:    service,
		Message:    record.Body,
		Level:      strings.ToLower(record.SeverityText),
		Attributes: stringAttrs,
	}

	if w.trace {
		v, _ := json.Marshal(e)
		w.log.Debug("trace", "payload", string(v))
	}

	ret = append(ret, e)
	return ret
}

func (w *OtelFileWatcher) updateStats(events []common.LogEvent) {
	for _, e := range events {
		w.stats.NumMessages++

		switch e.Level {
		case "emergency", "fatal":
			w.stats.NumEmergencyMessages++
		case "alert":
			w.stats.NumAlertMessages++
		case "critical", "crit":
			w.stats.NumCriticalMessages++
		case "error", "err":
			w.stats.NumErrorMessages++
		case "warning", "warn":
			w.stats.NumWarnMessages++
		case "notice":
			w.stats.NumNoticeMessages++
		case "info":
			w.stats.NumInfoMessages++
		case "debug":
			w.stats.NumDebugMessages++
		default:
			w.stats.NumInfoMessages++
		}

		if e.Service == "dnsmasq.service" || e.Service == "dnsmasq" {
			if strings.HasPrefix(e.Message, "dnsmasq-dhcp:") {
				if strings.Contains(e.Message, "DHCPDISCOVER") {
					w.stats.NumDHCPDiscover++
				} else if strings.Contains(e.Message, "DHCPOFFER") {
					w.stats.NumDHCPLeased++
				}
			} else if strings.HasPrefix(e.Message, "query") {
				w.stats.NumDNSQueries++
			} else if strings.HasPrefix(e.Message, "dnsmasq: config") {
				w.stats.NumDNSLocal++
			} else if strings.HasPrefix(e.Message, "forwarded") {
				w.stats.NumDNSRecursions++
			} else if strings.HasPrefix(e.Message, "cached") {
				w.stats.NumDNSCached++
			}

		} else if e.Node == "wally" && e.Service == "kernel" {
			if strings.Contains(e.Message, "drop wan in") {
				w.stats.NumFirewallWanInDrops++
				w.stats.NumFirewallWanDrops++
			} else if strings.Contains(e.Message, "drop wan out") {
				w.stats.NumFirewallWanOutDrops++
				w.stats.NumFirewallWanDrops++
			} else if strings.Contains(e.Message, "drop lan in") {
				w.stats.NumFirewallLanInDrops++
				w.stats.NumFirewallLanDrops++
			} else if strings.Contains(e.Message, "drop lan out") {
				w.stats.NumFirewallLanOutDrops++
				w.stats.NumFirewallLanDrops++
			}

		} else if strings.HasPrefix(e.Message, "Starting cert-renewer") {
			w.stats.NumCertChecks++
		} else if e.Message == "certificate does not need renewal" {
			w.stats.NumCertOK++
		} else if e.Service == "step-ca.service" && strings.Contains(e.Message, "path=/sign") && strings.Contains(e.Message, "status=201") {
			w.stats.NumCertSigned++
		} else if e.Service == "step-ca.service" && strings.Contains(e.Message, "path=/renew") && strings.Contains(e.Message, "status=201") {
			w.stats.NumCertRenewed++
		}

		if e.Service == "apache2" {
			if uri, ok := e.Attributes["uri"]; ok {
				if strings.Contains(uri, "/nodes-ipxe/lab/16") {
					w.stats.NumPhysicalPXEBoots++
				} else if strings.Contains(uri, "/nodes-ipxe/lab/de") {
					w.stats.NumVirtualPXEBoots++
				}
			}
		}
	}
}
