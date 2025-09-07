package loadstathandler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var rateTickerInterval = time.Millisecond * 100

const emaAlpha = 0.2 // Smoothing factor for EMA

type LoadStatReceiveHandler struct {
	stats        *LoadStats
	statChan     chan<- LoadStats
	clients      map[string]chan<- LoadStats
	log          slog.Logger
	clientsMutex sync.Mutex
	dataMutex    sync.RWMutex
}

type LoadStatSendHandler struct {
	mainHandler    *LoadStatReceiveHandler
	controlContext context.Context
	log            slog.Logger
}

type LoadStats struct {
	TotalServerOk  int
	TotalServerNok int
	TotalClientOk  int
	TotalClientNok int
	ServerOkRate   int
	ServerNokRate  int
	ClientOkRate   int
	ClientNokRate  int
	Servers        map[string]OKNOK
	Clients        map[string]OKNOK
}

type OKNOK struct {
	OK  int
	NOK int
}

var u = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func NewStatHandlers(controlContext context.Context, l *slog.Logger) (*LoadStatReceiveHandler, *LoadStatSendHandler, error) {
	incomingStatChan := make(chan LoadStats, 10)
	clients := make(map[string]chan<- LoadStats)

	receiveHandler := &LoadStatReceiveHandler{
		stats:    &LoadStats{Clients: make(map[string]OKNOK), Servers: make(map[string]OKNOK)},
		statChan: incomingStatChan,
		clients:  clients,
		log:      *l.With("operation", "LoadStatHandler"),
	}

	go func() {
		for {
			select {
			case <-controlContext.Done():
				return
			case stat := <-incomingStatChan:
				if len(clients) > 0 {
					receiveHandler.dataMutex.RLock()
					statCopy := LoadStats{
						Clients: make(map[string]OKNOK),
						Servers: make(map[string]OKNOK),
					}
					for k, v := range stat.Clients {
						statCopy.Clients[k] = v
					}
					for k, v := range stat.Servers {
						statCopy.Servers[k] = v
					}
					receiveHandler.dataMutex.RUnlock()

					receiveHandler.clientsMutex.Lock()
					clientsCopy := make([]chan<- LoadStats, 0, len(clients))
					for _, ch := range clients {
						clientsCopy = append(clientsCopy, ch)
					}
					receiveHandler.clientsMutex.Unlock()

					for _, ch := range clientsCopy {
						select {
						case ch <- statCopy:
						default:
							// Skip slow clients
						}
					}
				}
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(rateTickerInterval)
		defer ticker.Stop()
		var prev LoadStats
		var prevTime = time.Now()
		for {
			select {
			case <-controlContext.Done():
				return
			case <-ticker.C:
				receiveHandler.dataMutex.Lock()
				now := time.Now()
				elapsed := now.Sub(prevTime).Seconds()
				if elapsed > 0 {
					rawServerOkRate := float64(receiveHandler.stats.TotalServerOk-prev.TotalServerOk) / elapsed
					rawServerNokRate := float64(receiveHandler.stats.TotalServerNok-prev.TotalServerNok) / elapsed
					rawClientOkRate := float64(receiveHandler.stats.TotalClientOk-prev.TotalClientOk) / elapsed
					rawClientNokRate := float64(receiveHandler.stats.TotalClientNok-prev.TotalClientNok) / elapsed

					// EMA smoothing
					receiveHandler.stats.ServerOkRate = int(emaAlpha*rawServerOkRate + (1-emaAlpha)*float64(receiveHandler.stats.ServerOkRate))
					receiveHandler.stats.ServerNokRate = int(emaAlpha*rawServerNokRate + (1-emaAlpha)*float64(receiveHandler.stats.ServerNokRate))
					receiveHandler.stats.ClientOkRate = int(emaAlpha*rawClientOkRate + (1-emaAlpha)*float64(receiveHandler.stats.ClientOkRate))
					receiveHandler.stats.ClientNokRate = int(emaAlpha*rawClientNokRate + (1-emaAlpha)*float64(receiveHandler.stats.ClientNokRate))
				}
				prev = *receiveHandler.stats
				prevTime = now
				receiveHandler.dataMutex.Unlock()
			}
		}
	}()

	return receiveHandler,
		&LoadStatSendHandler{
			mainHandler:    receiveHandler,
			controlContext: controlContext,
			log:            *l.With("operation", "LoadStatHandler"),
		},
		nil
}

func (h *LoadStatReceiveHandler) addStatClient(id string, ch chan<- LoadStats) {
	h.clientsMutex.Lock()
	h.clients[id] = ch
	h.clientsMutex.Unlock()
}

func (h *LoadStatReceiveHandler) removeStatClient(id string) {
	h.clientsMutex.Lock()
	delete(h.clients, id)
	h.clientsMutex.Unlock()
}

func (h *LoadStatReceiveHandler) Reset() {
	var resetStats LoadStats

	h.dataMutex.Lock()
	// Reset all totals and rates
	h.stats.TotalServerOk = 0
	h.stats.TotalServerNok = 0
	h.stats.TotalClientOk = 0
	h.stats.TotalClientNok = 0
	h.stats.ServerOkRate = 0
	h.stats.ServerNokRate = 0
	h.stats.ClientOkRate = 0
	h.stats.ClientNokRate = 0

	// Reset all server stats
	for podName := range h.stats.Servers {
		h.stats.Servers[podName] = OKNOK{OK: 0, NOK: 0}
	}

	// Reset all client stats
	for podName := range h.stats.Clients {
		h.stats.Clients[podName] = OKNOK{OK: 0, NOK: 0}
	}

	resetStats = *h.stats
	h.dataMutex.Unlock()

	// Broadcast the reset
	h.statChan <- resetStats
}

func (h *LoadStatReceiveHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := u.Upgrade(w, r, nil)
	if err != nil {
		slog.Info("upgrade failed", "error", err.Error())
		return
	}
	defer conn.Close()

	stat := OKNOK{OK: 0, NOK: 0}

	_, tmp, err := conn.ReadMessage()
	if err != nil {
		h.log.Debug("read error", "error", err.Error())
		return
	}
	input := strings.Split(string(tmp), ":")
	if len(input) != 2 {
		h.log.Warn("invalid initial message received", "message", string(tmp))
		return
	}

	podType := input[0]
	podName := input[1]
	server := podType == "server"

	h.dataMutex.Lock()
	if server {
		h.stats.Servers[podName] = OKNOK{OK: 0, NOK: 0}
	} else {
		h.stats.Clients[podName] = OKNOK{OK: 0, NOK: 0}
	}

	h.dataMutex.Unlock()
	defer func() {
		h.dataMutex.Lock()
		if server {
			delete(h.stats.Servers, podName)
		} else {
			delete(h.stats.Clients, podName)
		}
		h.dataMutex.Unlock()
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			h.log.Debug("read error", "error", err.Error())
			break
		}

		input := strings.Split(string(message), ",")
		if len(input) != 2 {
			h.log.Warn("invalid message received", "message", string(message))
			continue
		}
		ok, err := strconv.Atoi(input[0])
		if err != nil {
			h.log.Warn("invalid message received", "message", string(message), "error", err.Error())
			continue
		}
		nok, err := strconv.Atoi(input[1])
		if err != nil {
			h.log.Warn("invalid message received", "message", string(message), "error", err.Error())
			continue
		}

		if ok == stat.OK && nok == stat.NOK {
			continue
		}

		incrementOk := ok - stat.OK
		incrementNok := nok - stat.NOK
		stat.OK = ok
		stat.NOK = nok

		h.dataMutex.Lock()
		if server {
			h.stats.TotalServerOk += incrementOk
			h.stats.TotalServerNok += incrementNok
			h.stats.Servers[podName] = stat
		} else {
			h.stats.TotalClientOk += incrementOk
			h.stats.TotalClientNok += incrementNok
			h.stats.Clients[podName] = stat
		}
		currentStats := *h.stats // Take a copy while holding the lock
		h.dataMutex.Unlock()

		h.statChan <- currentStats
	}
}

// getStatsCopy creates a deep copy of LoadStats to prevent concurrent map access during JSON marshaling
func (h *LoadStatReceiveHandler) getStatsCopy() LoadStats {
	h.dataMutex.RLock()
	defer h.dataMutex.RUnlock()

	safeCopy := *h.stats // Copy the basic fields

	// Deep copy Servers map
	safeCopy.Servers = make(map[string]OKNOK)
	for k, v := range h.stats.Servers {
		safeCopy.Servers[k] = v
	}

	// Deep copy Clients map
	safeCopy.Clients = make(map[string]OKNOK)
	for k, v := range h.stats.Clients {
		safeCopy.Clients[k] = v
	}

	return safeCopy
}

func (h *LoadStatSendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") == "" {
		// Use safe copy for JSON marshaling
		stats := h.mainHandler.getStatsCopy()
		b, _ := json.Marshal(stats)
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
