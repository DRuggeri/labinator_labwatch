package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/DRuggeri/labwatch/watchers/loki"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	trace := true
	printEvents := true
	printStats := false

	w, err := loki.NewLokiWatcher(context.Background(), "boss.local:3100", "", trace, log)
	if err != nil {
		panic(err)
	}

	events := make(chan loki.LogEvent)
	stats := make(chan loki.LogStats)
	go w.Watch(context.Background(), events, stats)

	for {
		var b []byte
		select {
		case e, ok := <-events:
			if ok && printEvents {
				b, _ = json.MarshalIndent(e, "", "  ")
			}
		case s, ok := <-stats:
			if ok && printStats {
				b, _ = json.MarshalIndent(s, "", "  ")
			}
		default:
			time.Sleep(time.Millisecond * 100)
			continue
		}

		if b != nil {
			fmt.Println(string(b))
		}
	}
}
