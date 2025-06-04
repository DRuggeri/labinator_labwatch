package callbacks

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
)

type CallbackStatus struct {
	KVPairs     map[string]map[string]string
	ClientCount map[string]int
}

type CallbackWatcher struct {
	statusChan  chan CallbackStatus
	kvPairs     map[string]map[string]string
	clientCount map[string]int
	mux         *sync.Mutex
	log         *slog.Logger
}

func NewCallbackWatcher(ctx context.Context, log *slog.Logger) (*CallbackWatcher, error) {
	if log == nil {
		log = slog.Default()
	}

	return &CallbackWatcher{
		statusChan:  make(chan CallbackStatus),
		kvPairs:     map[string]map[string]string{},
		clientCount: map[string]int{},
		mux:         &sync.Mutex{},
		log:         log.With("operation", "CallbackWatcher"),
	}, nil
}

func (cbw *CallbackWatcher) GetStatusUpdateChan() <-chan CallbackStatus {
	return cbw.statusChan
}

func (cbw *CallbackWatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	client := r.RemoteAddr
	if strings.Contains(client, ":") {
		tmp := strings.Split(client, ":")
		client = tmp[0]
	}

	cbw.clientCount[client]++

	key := r.URL.Query().Get("key")
	val := r.URL.Query().Get("val")

	cbw.log.Debug("callback received", "client", client, "key", key, "val", val)

	cbw.mux.Lock()
	defer cbw.mux.Unlock()

	clientMap, ok := cbw.kvPairs[client]
	if !ok {
		cbw.kvPairs[client] = map[string]string{}
		clientMap = cbw.kvPairs[client]
	}

	if key != "" && val != "" {
		clientMap[key] = val
	}

	status := CallbackStatus{
		KVPairs:     map[string]map[string]string{},
		ClientCount: map[string]int{},
	}

	for client, kvPairs := range cbw.kvPairs {
		status.KVPairs[client] = map[string]string{}
		for k, v := range kvPairs {
			status.KVPairs[client][k] = v
		}
	}
	for client, num := range cbw.clientCount {
		status.ClientCount[client] = num
	}

	cbw.log.Debug("dispatching callback to chan")
	cbw.statusChan <- status
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("callback received"))
}

func (cbw *CallbackWatcher) Reset() {
	cbw.mux.Lock()
	defer cbw.mux.Unlock()

	cbw.kvPairs = map[string]map[string]string{}
	cbw.clientCount = map[string]int{}
}
