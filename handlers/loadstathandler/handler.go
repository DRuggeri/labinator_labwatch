package loadstathandler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type LoadStatReceiveHandler struct {
	stats    *LoadStats
	statChan chan<- LoadStats
	clients  map[string]chan<- LoadStats
	log      slog.Logger
	mux      *sync.Mutex
}

type LoadStatSendHandler struct {
	mainHandler    *LoadStatReceiveHandler
	controlContext context.Context
	log            slog.Logger
}

type LoadStats struct {
	Total   int
	Clients map[string]int
}

var u = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func NewStatHandlers(controlContext context.Context, l *slog.Logger) (*LoadStatReceiveHandler, *LoadStatSendHandler, error) {
	incomingStatChan := make(chan LoadStats, 5)
	clients := make(map[string]chan<- LoadStats)

	go func() {
		for {
			select {
			case <-controlContext.Done():
				return
			case stat := <-incomingStatChan:
				for _, ch := range clients {
					ch <- stat
				}
			}
		}
	}()

	receiveHandler := &LoadStatReceiveHandler{
		stats:    &LoadStats{Total: 0, Clients: make(map[string]int)},
		statChan: incomingStatChan,
		clients:  clients,
		log:      *l.With("operation", "LoadStatHandler"),
		mux:      &sync.Mutex{},
	}

	return receiveHandler,
		&LoadStatSendHandler{
			mainHandler:    receiveHandler,
			controlContext: controlContext,
			log:            *l.With("operation", "LoadStatHandler"),
		},
		nil
}

func (h *LoadStatReceiveHandler) addStatClient(id string, ch chan<- LoadStats) {
	h.mux.Lock()
	h.clients[id] = ch
	h.mux.Unlock()
}

func (h *LoadStatReceiveHandler) removeStatClient(id string) {
	h.mux.Lock()
	delete(h.clients, id)
	h.mux.Unlock()
}

func (h *LoadStatReceiveHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := u.Upgrade(w, r, nil)
	if err != nil {
		slog.Info("upgrade failed", "error", err.Error())
		return
	}
	defer conn.Close()

	last := 0

	_, tmp, err := conn.ReadMessage()
	if err != nil {
		h.log.Debug("read error", "error", err.Error())
		return
	}
	podName := string(tmp)

	h.mux.Lock()
	h.stats.Clients[podName] = 0
	h.mux.Unlock()
	defer func() {
		h.mux.Lock()
		delete(h.stats.Clients, podName)
		h.mux.Unlock()
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			h.log.Debug("read error", "error", err.Error())
			break
		}

		total, err := strconv.Atoi(string(message))
		if err != nil {
			h.log.Warn("invalid message received", "message", string(message), "error", err.Error())
			continue
		}

		if total == last {
			continue
		}

		increment := total - last
		last = total

		h.mux.Lock()
		h.stats.Total += increment
		h.stats.Clients[podName] = total
		h.mux.Unlock()
		h.statChan <- *h.stats
	}
}

func (h *LoadStatSendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") == "" {
		b, _ := json.Marshal(h.mainHandler.stats)
		w.Write(b)
		return
	}

	conn, err := u.Upgrade(w, r, nil)
	if err != nil {
		slog.Info("upgrade failed", "error", err.Error())
		return
	}
	defer conn.Close()

	ch := make(chan LoadStats, 5)
	uuid := uuid.New().String()

	h.mainHandler.addStatClient(uuid, ch)
	defer h.mainHandler.removeStatClient(uuid)

	for {
		select {
		case <-h.controlContext.Done():
			return
		case stat := <-ch:
			if err := conn.WriteJSON(stat); err != nil {
				h.log.Debug("write error", "error", err.Error())
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}
