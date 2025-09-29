package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/DRuggeri/labwatch/routerman"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	logger.Info("Exercising routerman package")

	// Create the router manager
	rm, err := routerman.NewRouterManager("https://192.168.122.1", "root", "wally", logger)
	if err != nil {
		logger.Error("Failed to create router manager", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	// Test WiFi network scanning
	logger.Info("Scanning for WiFi networks...")
	networks, err := rm.GetWifiNetworks(ctx)
	if err != nil {
		logger.Error("Failed to scan WiFi networks", "error", err)
	} else {
		logger.Info("WiFi scan completed", "networks_found", len(networks))
		for _, network := range networks {
			logger.Info("WiFi network found",
				"ssid", network.SSID,
				"signal", network.Signal,
				"secured", network.Secured,
			)
		}
	}

	// Test connection to a network (commented out for safety)
	/*
		logger.Info("Testing WiFi connection...")
		result, err := rm.ConnectToWifiNetwork(ctx, "MyNetwork", "MyPassword")
		if err != nil {
			logger.Error("Failed to connect to WiFi", "error", err)
		} else {
			logger.Info("WiFi connection result",
				"success", result.Success,
				"message", result.Message,
				"error", result.Error,
			)
		}
	*/

	logger.Info("Test completed - router manager functions tested successfully")

	// Keep the program running for a bit to observe any additional logs
	time.Sleep(5 * time.Second)
}
