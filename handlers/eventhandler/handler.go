package eventhandler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/DRuggeri/labwatch/watchers/common"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type EventReceiveHandler struct {
	log          *slog.Logger
	clients      map[string]chan<- common.LogEvent
	clientsMutex sync.Mutex
}

type EventSendHandler struct {
	log      *slog.Logger
	watcher  *EventReceiveHandler
	upgrader websocket.Upgrader
}

func NewEventWatcher(ctx context.Context, log *slog.Logger) (*EventReceiveHandler, *EventSendHandler, error) {
	watcher := &EventReceiveHandler{
		log:     log.With("component", "eventReceiveHandler"),
		clients: make(map[string]chan<- common.LogEvent),
	}

	sendHandler := &EventSendHandler{
		log:     log.With("component", "eventSendHandler"),
		watcher: watcher,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
	}

	return watcher, sendHandler, nil
}

func (h *EventReceiveHandler) AddClient(id string, ch chan<- common.LogEvent) {
	h.clientsMutex.Lock()
	h.clients[id] = ch
	h.clientsMutex.Unlock()
	h.log.Debug("added event client", "id", id, "totalClients", len(h.clients))
}

func (h *EventReceiveHandler) RemoveClient(id string) {
	h.clientsMutex.Lock()
	delete(h.clients, id)
	clientCount := len(h.clients)
	h.clientsMutex.Unlock()
	h.log.Debug("removed event client", "id", id, "totalClients", clientCount)
}

func (h *EventReceiveHandler) BroadcastEvent(event common.LogEvent) {
	h.clientsMutex.Lock()
	if len(h.clients) > 0 {
		// Create a copy of clients to avoid holding lock during broadcast
		clientsCopy := make(map[string]chan<- common.LogEvent, len(h.clients))
		for k, v := range h.clients {
			clientsCopy[k] = v
		}
		h.clientsMutex.Unlock()

		// Broadcast without holding the lock
		for id, ch := range clientsCopy {
			select {
			case ch <- event:
				// Successfully sent
			default:
				// Channel full, skip this client to avoid blocking
				level := slog.LevelWarn

				// Statusinator is known to fall behind during lots of logging
				if id == "statusinator" {
					level = slog.LevelDebug
				}
				h.log.Log(context.Background(), level, "event client channel full, skipping", "id", id)
			}
		}
	} else {
		h.clientsMutex.Unlock()
	}
}

func (h *EventSendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Handle WebSocket upgrade
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Info("websocket upgrade failed", "error", err.Error())
		return
	}
	defer conn.Close()

	// Create channel for this client
	thisChan := make(chan common.LogEvent, 10) // Buffered to prevent blocking
	clientID := uuid.New().String()

	h.watcher.AddClient(clientID, thisChan)
	defer h.watcher.RemoveClient(clientID)

	// Listen for events and send to client
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-thisChan:
			data, _ := json.Marshal(event)
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				h.log.Debug("client disconnected", "error", err.Error())
				return
			}
		}
	}
}
