package statushandler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/DRuggeri/labwatch/watchers/callbacks"
	"github.com/DRuggeri/labwatch/watchers/common"
	"github.com/DRuggeri/labwatch/watchers/kubernetes"
	"github.com/DRuggeri/labwatch/watchers/port"
	"github.com/DRuggeri/labwatch/watchers/talos"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type StatusWatcher struct {
	log           *slog.Logger
	clients       map[string]chan<- common.LabStatus
	clientsMutex  sync.Mutex
	currentStatus common.LabStatus
	currentMutex  sync.RWMutex
}

type StatusSendHandler struct {
	log      *slog.Logger
	watcher  *StatusWatcher
	upgrader websocket.Upgrader
}

func NewStatusWatcher(ctx context.Context, log *slog.Logger) (*StatusWatcher, *StatusSendHandler, error) {
	watcher := &StatusWatcher{
		log:     log.With("component", "statusReceiveHandler"),
		clients: make(map[string]chan<- common.LabStatus),
	}

	sendHandler := &StatusSendHandler{
		log:     log.With("component", "statusSendHandler"),
		watcher: watcher,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
	}

	return watcher, sendHandler, nil
}

func (h *StatusWatcher) AddClient(id string, ch chan<- common.LabStatus) {
	h.clientsMutex.Lock()
	h.clients[id] = ch
	h.clientsMutex.Unlock()
	h.log.Debug("added status client", "id", id, "totalClients", len(h.clients))
}

func (h *StatusWatcher) RemoveClient(id string) {
	h.clientsMutex.Lock()
	delete(h.clients, id)
	clientCount := len(h.clients)
	h.clientsMutex.Unlock()
	h.log.Debug("removed status client", "id", id, "totalClients", clientCount)
}

func (h *StatusWatcher) UpdateStatus(status common.LabStatus) {
	// Create a deep copy to ensure thread safety
	safeCopy := h.safeCopyForMarshaling(status)

	// Update current status
	h.currentMutex.Lock()
	h.currentStatus = safeCopy
	h.currentMutex.Unlock()

	h.clientsMutex.Lock()
	if len(h.clients) > 0 {
		h.log.Debug("broadcasting status", "clients", len(h.clients))

		// Create a copy of clients to avoid holding lock during broadcast
		clientsCopy := make(map[string]chan<- common.LabStatus, len(h.clients))
		for k, v := range h.clients {
			clientsCopy[k] = v
		}
		h.clientsMutex.Unlock()

		// Broadcast without holding the lock
		for id, ch := range clientsCopy {
			select {
			case ch <- safeCopy:
				// Successfully sent
			default:
				// Channel full, skip this client to avoid blocking
				// Don't warn about statusinator which can get overwhelmed from lots of logs
				if id != "statusinator" {
					h.log.Warn("status client channel full, skipping", "id", id)
				}
			}
		}
	} else {
		h.clientsMutex.Unlock()
	}
}

func (h *StatusWatcher) ResetStatus() {
	h.UpdateStatus(common.LabStatus{})
}

func (h *StatusWatcher) GetCurrentStatus() common.LabStatus {
	h.currentMutex.RLock()
	status := h.currentStatus
	h.currentMutex.RUnlock()
	return status
}

// safeCopyForMarshaling creates a deep copy of LabStatus for safe JSON marshaling
// This prevents concurrent map access during JSON serialization
func (h *StatusWatcher) safeCopyForMarshaling(status common.LabStatus) common.LabStatus {
	safeCopy := status // Copy all scalar fields

	// Deep copy Kubernetes map
	if status.Kubernetes != nil {
		safeCopy.Kubernetes = make(map[string]kubernetes.PodStatus, len(status.Kubernetes))
		for k, v := range status.Kubernetes {
			safeCopy.Kubernetes[k] = v
		}
	}

	// Deep copy Talos map
	if status.Talos != nil {
		safeCopy.Talos = make(map[string]talos.NodeStatus, len(status.Talos))
		for k, v := range status.Talos {
			safeCopy.Talos[k] = v
		}
	}

	// Deep copy Ports map
	if status.Ports != nil {
		safeCopy.Ports = make(port.PortStatus, len(status.Ports))
		for k, v := range status.Ports {
			safeCopy.Ports[k] = v
		}
	}

	// Deep copy Callbacks structure
	if status.Callbacks.KVPairs != nil || status.Callbacks.ClientCount != nil {
		safeCopy.Callbacks = callbacks.CallbackStatus{
			KVPairs:     make(map[string]map[string]string),
			ClientCount: make(map[string]int),
		}
		for k, v := range status.Callbacks.KVPairs {
			safeCopy.Callbacks.KVPairs[k] = make(map[string]string)
			for k2, v2 := range v {
				safeCopy.Callbacks.KVPairs[k][k2] = v2
			}
		}
		for k, v := range status.Callbacks.ClientCount {
			safeCopy.Callbacks.ClientCount[k] = v
		}
	}

	return safeCopy
}

func (h *StatusSendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Handle non-WebSocket requests (return current status as JSON)
	if r.Header.Get("Upgrade") == "" {
		status := h.watcher.GetCurrentStatus()
		safeCopy := h.watcher.safeCopyForMarshaling(status)
		b, _ := json.Marshal(safeCopy)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
		return
	}

	// Handle WebSocket upgrade
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Info("websocket upgrade failed", "error", err.Error())
		return
	}
	defer conn.Close()

	// Create channel for this client
	thisChan := make(chan common.LabStatus, 5) // Buffered to prevent blocking
	clientID := uuid.New().String()

	h.watcher.AddClient(clientID, thisChan)
	defer h.watcher.RemoveClient(clientID)

	// Send current status immediately
	currentStatus := h.watcher.GetCurrentStatus()
	safeCopy := h.watcher.safeCopyForMarshaling(currentStatus)
	data, _ := json.Marshal(safeCopy)
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		h.log.Info("failed to send initial status", "error", err.Error())
		return
	}

	// Listen for status updates and send to client
	for {
		select {
		case <-r.Context().Done():
			return
		case status := <-thisChan:
			safeCopy := h.watcher.safeCopyForMarshaling(status)
			data, _ := json.Marshal(safeCopy)
			conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				h.log.Debug("client disconnected", "error", err.Error())
				return
			}
		}
	}
}
