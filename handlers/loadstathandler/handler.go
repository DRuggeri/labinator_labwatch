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
	incomingStatChan := make(chan LoadStats, 5)
	clients := make(map[string]chan<- LoadStats)
	mux := &sync.Mutex{}

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

	stats := &LoadStats{Clients: make(map[string]OKNOK), Servers: make(map[string]OKNOK)}
	receiveHandler := &LoadStatReceiveHandler{
		stats:    stats,
		statChan: incomingStatChan,
		clients:  clients,
		log:      *l.With("operation", "LoadStatHandler"),
		mux:      mux,
	}

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
				mux.Lock()
				now := time.Now()
				elapsed := now.Sub(prevTime).Seconds()
				if elapsed > 0 {
					rawServerOkRate := float64(stats.TotalServerOk-prev.TotalServerOk) / elapsed
					rawServerNokRate := float64(stats.TotalServerNok-prev.TotalServerNok) / elapsed
					rawClientOkRate := float64(stats.TotalClientOk-prev.TotalClientOk) / elapsed
					rawClientNokRate := float64(stats.TotalClientNok-prev.TotalClientNok) / elapsed

					// EMA smoothing
					stats.ServerOkRate = int(emaAlpha*rawServerOkRate + (1-emaAlpha)*float64(stats.ServerOkRate))
					stats.ServerNokRate = int(emaAlpha*rawServerNokRate + (1-emaAlpha)*float64(stats.ServerNokRate))
					stats.ClientOkRate = int(emaAlpha*rawClientOkRate + (1-emaAlpha)*float64(stats.ClientOkRate))
					stats.ClientNokRate = int(emaAlpha*rawClientNokRate + (1-emaAlpha)*float64(stats.ClientNokRate))
				}
				prev = *stats
				prevTime = now
				mux.Unlock()
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

	h.mux.Lock()
	if server {
		h.stats.Servers[podName] = OKNOK{OK: 0, NOK: 0}
	} else {
		h.stats.Clients[podName] = OKNOK{OK: 0, NOK: 0}
	}

	h.mux.Unlock()
	defer func() {
		h.mux.Lock()
		if server {
			delete(h.stats.Servers, podName)
		} else {
			delete(h.stats.Clients, podName)
		}
		h.mux.Unlock()
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

		h.mux.Lock()
		if server {
			h.stats.TotalServerOk += incrementOk
			h.stats.TotalServerNok += incrementNok
			h.stats.Servers[podName] = stat
		} else {
			h.stats.TotalClientOk += incrementOk
			h.stats.TotalClientNok += incrementNok
			h.stats.Clients[podName] = stat
		}
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
			h.mainHandler.mux.Lock()
			if err := conn.WriteJSON(stat); err != nil {
				h.log.Debug("write error", "error", err.Error())
				h.mainHandler.mux.Unlock()
				return
			}
			h.mainHandler.mux.Unlock()
		case <-r.Context().Done():
			return
		}
	}
}
