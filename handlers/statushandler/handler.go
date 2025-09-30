package statushandler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/DRuggeri/labwatch/watchers/common"
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
	safeCopy := status.Clone()

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
				if id != "statusinator" && id != "reliabilitytest" {
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

func (h *StatusSendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Handle non-WebSocket requests (return current status as JSON)
	if r.Header.Get("Upgrade") == "" {
		status := h.watcher.GetCurrentStatus()
		safeCopy := status.Clone()
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
	safeCopy := currentStatus.Clone()
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
			safeCopy := status.Clone()
			data, _ := json.Marshal(safeCopy)
			conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				h.log.Debug("client disconnected", "error", err.Error())
				return
			}
		}
	}
}
