package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/DRuggeri/labwatch/watchers/common"
	"github.com/DRuggeri/labwatch/watchers/otelfile"
)

func main() {
	var path string
	var trace bool

	flag.StringVar(&path, "path", "", "Path to the OTEL log file to watch")
	flag.BoolVar(&trace, "trace", false, "Enable trace logging")
	flag.Parse()

	if path == "" {
		fmt.Fprintf(os.Stderr, "Error: --path is required\n")
		flag.Usage()
		os.Exit(1)
	}

	// Set up logging
	logLevel := slog.LevelInfo
	if trace {
		logLevel = slog.LevelDebug
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))

	log.Info("starting OTEL file watcher", "path", path, "trace", trace)

	// Create watcher
	watcher, err := otelfile.NewOtelFileWatcher(context.Background(), path, trace, log)
	if err != nil {
		log.Error("failed to create watcher", "error", err)
		os.Exit(1)
	}

	// Set up channels
	eventChan := make(chan common.LogEvent, 1000)
	statChan := make(chan common.LogStats, 100)

	// Set up context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Info("shutdown signal received")
		cancel()
	}()

	// Start watcher
	go watcher.Watch(ctx, eventChan, statChan)

	// Process events
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		case event := <-eventChan:
			log.Info("event received",
				"node", event.Node,
				"service", event.Service,
				"level", event.Level,
				"message", event.Message,
				"attributes", len(event.Attributes))
		case stats := <-statChan:
			log.Info("stats updated",
				"totalMessages", stats.NumMessages,
				"emergency", stats.NumEmergencyMessages,
				"alert", stats.NumAlertMessages,
				"critical", stats.NumCriticalMessages,
				"error", stats.NumErrorMessages,
				"warning", stats.NumWarnMessages,
				"notice", stats.NumNoticeMessages,
				"info", stats.NumInfoMessages,
				"debug", stats.NumDebugMessages)
		}
	}
}
