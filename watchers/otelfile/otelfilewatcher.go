package otelfile

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/DRuggeri/labwatch/watchers/common"
)

var sleepDuration = time.Duration(10) * time.Millisecond
var fileCheckDuration = time.Duration(1) * time.Second
var statUpdateThrottle = time.Duration(100) * time.Millisecond

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
	reader           *bufio.Reader
	lastFileInfo     os.FileInfo
	lineBuffer       []byte
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

func (w *OtelFileWatcher) openFile(tail bool) error {
	if w.currentFile != nil {
		w.currentFile.Close()
		w.currentFile = nil
		w.reader = nil
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
	w.reader = bufio.NewReader(file)
	w.lineBuffer = make([]byte, 0, 4096) // Initialize line buffer

	// Seek to end of file to start tailing on first start - otherwise, read the whole file
	if tail {
		w.currentFile.Seek(0, 2)
		// Reset reader after seek
		w.reader = bufio.NewReader(w.currentFile)
	}

	w.log.Info("opened file for tailing", "path", w.path, "size", info.Size(), "tail", tail)
	return nil
}

// readLine reads a complete line from the file, handling EOF gracefully for tailing
func (w *OtelFileWatcher) readLine() (string, error) {
	if w.reader == nil {
		return "", fmt.Errorf("reader not initialized")
	}

	for {
		line, err := w.reader.ReadString('\n')
		if err == io.EOF && line != "" {
			// We got some data but hit EOF - save it for next time and return empty for now
			// This handles the case where a line is being written while we're reading
			return "", io.EOF
		}
		if err == io.EOF {
			// No data and EOF - this is normal for tailing, just return EOF
			return "", io.EOF
		}
		if err != nil {
			// Some other error
			return "", err
		}

		// Remove the newline character and return the line
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r") // Handle Windows line endings too

		if line != "" {
			return line, nil
		}
		// Empty line, continue reading
	}
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

	// Look at inode first
	if newStat, ok := info.Sys().(*syscall.Stat_t); ok {
		if oldStat, ok2 := w.lastFileInfo.Sys().(*syscall.Stat_t); ok2 {
			if newStat.Ino != oldStat.Ino {
				w.log.Debug("file rotation detected - different inode",
					"path", w.path,
					"oldInode", oldStat.Ino,
					"newInode", newStat.Ino)
				return true
			}
		}
	}

	// Check if size decreased (strong indicator of rotation)
	if info.Size() < w.lastFileInfo.Size() {
		w.log.Debug("file rotation detected - size decreased",
			"path", w.path,
			"oldSize", w.lastFileInfo.Size(),
			"newSize", info.Size())
		return true
	}

	// Check if modification time is significantly different
	if info.ModTime().Before(w.lastFileInfo.ModTime()) {
		w.log.Debug("file rotation detected - modification time went backwards",
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
			if w.currentFile == nil {
				if err := w.openFile(true); err != nil {
					w.log.Error("failed to open file", "error", err)
					time.Sleep(sleepDuration)
					continue
				}
				lastFileCheck = time.Now()
			} else if time.Since(lastFileCheck) > fileCheckDuration {
				if w.checkFileRotation() {
					w.log.Info("reopening file due to rotation")
					w.openFile(false)
				}
				lastFileCheck = time.Now()
			}

			// Try to read a line
			line, err := w.readLine()
			if err == io.EOF {
				// Reached EOF - normal for tailing, just sleep and try again
				if w.trace {
					w.log.Debug("reached EOF, waiting for new data")
				}
				time.Sleep(sleepDuration)
				continue
			} else if err != nil {
				w.log.Error("read error", "error", err)
				// Force file reopen on next iteration
				if w.currentFile != nil {
					w.currentFile.Close()
					w.currentFile = nil
					w.reader = nil
				}
				time.Sleep(sleepDuration)
				continue
			}

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
		}
	}()

	// Event forwarding loop
	lastStatusUpdate := time.Now()
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
		// and drain them all if so
	OUTER:
		for {
			select {
			case event := <-w.internalLogChan:
				select {
				case eventChan <- event:
					if w.trace {
						w.log.Debug("forwarded event to main channel")
					}
				default:
					w.log.Warn("event channel full, dropping event")
				}
			case stats := <-w.internalStatChan:
				if time.Since(lastStatusUpdate) < statUpdateThrottle {
					// Throttle stats updates
					break OUTER
				}
				lastStatusUpdate = time.Now()
				select {
				case statChan <- stats:
					if w.trace {
						w.log.Debug("forwarded stats to main channel")
					}
				default:
					w.log.Warn("stats channel full, dropping stats")
				}
			default:
				break OUTER
			}

			time.Sleep(5 * time.Millisecond)
		}
	}
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
