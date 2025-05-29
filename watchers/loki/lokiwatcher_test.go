package loki_test

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/DRuggeri/labwatch/statusinator"
	"github.com/DRuggeri/labwatch/watchers/loki"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fileDumper struct {
	log   *slog.Logger
	path  string
	Lines int
}

type logCLIFormat struct {
	Labels    map[string]string
	Line      string
	Timestamp string
}

type lokiStream struct {
	Stream map[string]string
	Values [][]string
}

func (f fileDumper) dumpFromFile(w http.ResponseWriter, r *http.Request) {
	f.log.Info("dumping", "file", f.path)
	u := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}

	conn, err := u.Upgrade(w, r, nil)
	if err != nil {
		f.log.Error("upgrade failed", "error", err.Error())
		return
	}
	f.log.Debug("connection upgraded to websocket")

	file, err := os.Open(f.path)
	if err != nil {
		f.log.Error("failed to read file", "error", err.Error())
		return
	}
	f.log.Debug("file opened")

	scanner := bufio.NewScanner(file)
	if err := scanner.Err(); err != nil {
		f.log.Error("failed to create scanner", "error", err.Error())
		return
	}
	f.log.Debug("scanner created")

	for scanner.Scan() {
		f.Lines = f.Lines + 1
		f.log.Debug("read a line", "total", f.Lines, "content", scanner.Text())

		in := logCLIFormat{}
		err = json.Unmarshal(scanner.Bytes(), &in)
		if err != nil {
			f.log.Error("failed to parse JSON input?!", "error", err.Error())
			return
		}
		out := map[string][]lokiStream{
			"streams": {{
				Stream: in.Labels,
				Values: [][]string{{in.Timestamp, in.Line}},
			}},
		}

		b, err := json.Marshal(out)
		if err != nil {
			f.log.Error("failed to parse JSON input?!", "error", err.Error())
			return
		}

		if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
			f.log.Error("failed to write message", "error", err.Error())
			return
		}
		f.log.Debug("wrote message to websocket")
	}
	file.Close()

	f.log.Debug("read completed", "lines", f.Lines)

	for {
		select {
		case <-r.Context().Done():
			return
		default:
			time.Sleep(time.Second)
		}
	}
}
func TestFromLogFile(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	d := &fileDumper{log: log.With("operation", "fileDumper"), path: "/root/lablog"}
	mux := http.NewServeMux()
	mux.HandleFunc("/loki/api/v1/tail", d.dumpFromFile)
	go http.ListenAndServeTLS("127.0.0.1:8181", "/etc/ssl/certs/loki.pem", "/etc/ssl/private/loki.key", mux)

	// Give it a second to start
	time.Sleep(time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	w, err := loki.NewLokiWatcher(ctx, "127.0.0.1:8181", "", true, log)
	require.NoError(t, err)

	events := make(chan loki.LogEvent)
	stats := make(chan loki.LogStats)

	go w.Watch(context.Background(), events, stats)

	status := statusinator.LabStatus{}
	errorsEncountered := false
	eventsEncountered := 0
	lastUpdate := time.Now()
	for time.Now().Before(lastUpdate.Add(time.Second)) {
		select {
		case s, ok := <-stats:
			lastUpdate = time.Now()
			if ok {
				status.Logs = s
			} else {
				errorsEncountered = true
			}
		case _, ok := <-events:
			lastUpdate = time.Now()
			if ok {
				eventsEncountered++
			} else {
				errorsEncountered = true
			}
		default:
			time.Sleep(time.Millisecond * 5)
			continue
		}
	}

	cancel()
	assert.False(t, errorsEncountered)
	assert.Equal(t, 18040, d.Lines)
	assert.Equal(t, 18040, status.Logs.NumMessages)
}
