package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/DRuggeri/labwatch/watchers/port"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	w, err := port.NewPortWatcher(context.Background(), []string{"127.0.0.1:22", "127.0.0.1:3000", "127.0.0.1:8443"}, log)
	if err != nil {
		panic(err)
	}

	info := make(chan port.PortStatus)
	go w.Watch(context.Background(), info)
	for {
		status := <-info
		fmt.Println(time.Now().String())

		for endpoint, status := range status {
			fmt.Printf("%s\t%t\n", endpoint, status)
		}
		fmt.Println()
	}
}
