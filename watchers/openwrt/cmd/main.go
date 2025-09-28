package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/DRuggeri/labwatch/watchers/openwrt"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	endpoint := "http://192.168.122.1:9100/metrics"
	w, err := openwrt.NewOpenWrtWatcher(context.Background(), endpoint, log)
	if err != nil {
		panic(err)
	}

	info := make(chan openwrt.OpenWrtStatus)
	go w.Watch(context.Background(), info)

	log.Info("Starting OpenWrt watcher", "endpoint", endpoint)
	log.Info("Waiting for status updates...")

	for {
		status := <-info
		log.Info("OpenWrt Status Update",
			"wan_in", status.NumWanIn,
			"wan_out", status.NumWanOut,
			"lan_in", status.NumLanIn,
			"lan_out", status.NumLanOut)
	}
}
