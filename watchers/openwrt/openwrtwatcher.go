package openwrt

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var statusRecheckInterval = 5 * time.Second

// OpenWrtStatus represents the status values from the OpenWrt router
type OpenWrtStatus struct {
	NumWanIn  uint64 `json:"NumWanIn"`
	NumWanOut uint64 `json:"NumWanOut"`
	NumLanIn  uint64 `json:"NumLanIn"`
	NumLanOut uint64 `json:"NumLanOut"`
}

type OpenWrtWatcher struct {
	endpoint string
	status   OpenWrtStatus
	mux      *sync.Mutex
	log      *slog.Logger
	client   *http.Client
}

func NewOpenWrtWatcher(ctx context.Context, endpoint string, log *slog.Logger) (*OpenWrtWatcher, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	return &OpenWrtWatcher{
		endpoint: endpoint,
		mux:      &sync.Mutex{},
		log:      log.With("operation", "OpenWrtWatcher"),
		client:   client,
	}, nil
}

func (w *OpenWrtWatcher) Watch(controlContext context.Context, resultChan chan<- OpenWrtStatus) {
	w.log.Info("starting OpenWrt monitoring", "endpoint", w.endpoint)

	go func() {
		for {
			select {
			case <-controlContext.Done():
				return
			default:
			}

			newStatus, err := w.checkOpenWrt()

			if err == nil {
				w.mux.Lock()
				// Check if status changed
				statusChanged := w.status.NumWanIn != newStatus.NumWanIn ||
					w.status.NumWanOut != newStatus.NumWanOut ||
					w.status.NumLanIn != newStatus.NumLanIn ||
					w.status.NumLanOut != newStatus.NumLanOut

				w.status = newStatus
				statusCopy := newStatus
				w.mux.Unlock()

				w.log.Debug("OpenWrt status checked",
					"endpoint", w.endpoint,
					"changed", statusChanged)

				if statusChanged {
					select {
					case resultChan <- statusCopy:
					case <-controlContext.Done():
						return
					default:
						w.log.Warn("OpenWrt status update dropped, channel full")
					}
				}
			} else {
				w.log.Debug("OpenWrt check failed", "endpoint", w.endpoint, "error", err)
			}

			select {
			case <-time.After(statusRecheckInterval):
			case <-controlContext.Done():
				return
			}
		}
	}()

	<-controlContext.Done()
	w.log.Info("stopping OpenWrt watcher")
}

func (w *OpenWrtWatcher) checkOpenWrt() (OpenWrtStatus, error) {
	status := OpenWrtStatus{}
	resp, err := w.client.Get(w.endpoint)
	if err != nil {
		w.log.Debug("OpenWrt connection failed", "endpoint", w.endpoint, "error", err)
		return status, fmt.Errorf("failed to connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		w.log.Debug("OpenWrt returned error", "endpoint", w.endpoint, "status", resp.StatusCode)
		return status, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)

	// Parse Prometheus exposition format the quick way
	networkPattern := regexp.MustCompile(`^node_network_(receive|transmit)_packets_total\{device="([^"]+)"\}\s+(\d+)`)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if matches := networkPattern.FindStringSubmatch(line); matches != nil {
			direction := matches[1] // "receive" or "transmit"
			device := matches[2]    // device name
			value, err := strconv.ParseUint(matches[3], 10, 64)
			if err != nil {
				w.log.Debug("Failed to parse network value", "line", line, "error", err)
				continue
			}

			switch direction {
			case "receive":
				switch device {
				case "eth0": // WAN interface
					status.NumWanIn = value
				case "eth1": // LAN interface
					status.NumLanIn = value
				}
			case "transmit":
				switch device {
				case "eth0": // WAN interface
					status.NumWanOut = value
				case "eth1": // LAN interface
					status.NumLanOut = value
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		w.log.Warn("OpenWrt failed to parse response", "endpoint", w.endpoint, "error", err)
		return status, fmt.Errorf("failed to parse response: %w", err)
	}

	w.log.Debug("OpenWrt check successful", "endpoint", w.endpoint,
		"wan_in", status.NumWanIn, "wan_out", status.NumWanOut,
		"lan_in", status.NumLanIn, "lan_out", status.NumLanOut)

	return status, nil
}
