package main

import (
	"log/slog"
	"os"

	"github.com/DRuggeri/labwatch/talosinitializer"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	_, err := talosinitializer.NewTalosInitializer("/home/boss/talos/scenarios/configs.yaml", "/home/boss/talos/scenarios", "koob", log)
	if err != nil {
		panic(err)
	}

	nodes := []talosinitializer.NodeConfig{
		{
			Name: "w6-hybrid",
			IP:   "192.168.122.36",
			MAC:  "de:ad:be:ef:30:06",
		},
	}
	talosinitializer.StartVMsOnHypervisor("192.168.122.16", nodes, log)
}
