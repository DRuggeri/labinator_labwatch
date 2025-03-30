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
	lvl := slog.LevelVar{}
	lvl.Set(slog.LevelDebug)
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	w, err := loki.NewLokiWatcher(context.Background(), "boss.local:3100", "", log)
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
			if ok {
				b, _ = json.MarshalIndent(e, "", "  ")
			}
		case s, ok := <-stats:
			if ok {
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
