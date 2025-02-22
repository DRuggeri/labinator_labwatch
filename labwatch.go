package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/DRuggeri/labwatch/watchers/talos"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type LabStatus struct {
	Talos map[string]talos.NodeStatus `json:"talos"`
}

var currentStatus = LabStatus{}
var statusClients = map[string]chan<- LabStatus{}
var lock = &sync.Mutex{}

func main() {
	slog.Info("statusinator")

	startWatchers()

	u := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}

	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		conn, err := u.Upgrade(w, r, nil)
		if err != nil {
			slog.Info("upgrade failed", "error", err.Error())
			return
		}

		thisChan := make(chan LabStatus)
		uuid := uuid.New().String()

		addClient(uuid, thisChan)
		defer removeClient(uuid)

		data, _ := json.Marshal(currentStatus)
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			slog.Info("write failed", "error", err.Error())
			return
		}

		for {
			var status LabStatus
			select {
			case <-r.Context().Done():
				return
			case status = <-thisChan:
			}
			data, _ := json.Marshal(status)
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "websockets.html")
	})

	http.ListenAndServe(":8080", nil)
}

func startWatchers() error {
	status := LabStatus{}

	tWatcher, err := talos.NewTalosWatcher(context.Background(), "/root/talos/talosconfig", "koobs")
	if err != nil {
		return err
	}

	tInfo := make(chan map[string]talos.NodeStatus)
	go tWatcher.Watch(context.Background(), tInfo)

	go func() {
		for {
			select {
			case t, ok := <-tInfo:
				if ok {
					status.Talos = t
				} else {
					return
				}
			default:
				time.Sleep(time.Millisecond * 100)
				continue
			}

			currentStatus = status
			slog.Info("broadcasting status", "clients", len(statusClients))
			for _, ch := range statusClients {
				ch <- status
			}
		}
	}()

	return nil
}

func addClient(id string, ch chan<- LabStatus) {
	lock.Lock()
	statusClients[id] = ch
	lock.Unlock()
}

func removeClient(id string) {
	lock.Lock()
	delete(statusClients, id)
	lock.Unlock()
}
