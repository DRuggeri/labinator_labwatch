package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/DRuggeri/labwatch/switchman"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	logger.Info("Exercising switchman package")

	// Create the switch manager
	sm, err := switchman.NewSwitchManager("http://192.168.122.2", logger)
	if err != nil {
		logger.Error("Failed to create switch manager", "error", err)
		os.Exit(1)
	}
	defer sm.Stop()

	// channel of switch status updates
	statusChan := make(chan switchman.SwitchStatus)

	// Start watching for status updates
	go sm.Watch(context.Background(), statusChan)

	// Spin off a goroutine watching for updates
	go func() {
		for {
			status, ok := <-statusChan
			if !ok {
				logger.Info("Status channel closed")
				return
			}
			logger.Info("Switch status updated", "status", status)
		}
	}()

	// Turn off 4
	sm.TurnOff(switchman.SP4)
	time.Sleep(10 * time.Second)

	// Turn on 4
	sm.TurnOn(switchman.SP4)
	time.Sleep(10 * time.Second)

	for {
		time.Sleep(1 * time.Second)
	}
}
