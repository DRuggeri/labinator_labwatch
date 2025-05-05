package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/DRuggeri/labwatch/watchers/talos"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	w, err := talos.NewTalosWatcher(context.Background(), "/home/boss/talos/talosconfig", "virtual-2", log)
	if err != nil {
		panic(err)
	}

	info := make(chan map[string]talos.NodeStatus)
	go w.Watch(context.Background(), info)
	for {
		status := <-info
		fmt.Println(time.Now().String())
		//fmt.Println("Node\t\tConn\tapid\tkubelt\tcontainerd\tAddrs")
		fmt.Println("Node\t\tConn\t\tAddrs")

		nodes := []string{}
		for node := range status {
			nodes = append(nodes, node)
		}
		sort.Strings(nodes)

		for _, node := range nodes {
			nodeStatus := status[node]
			//fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", nodeStatus.Node, nodeStatus.WatcherState, nodeStatus.Services["apid"].Healthy, nodeStatus.Services["kubelet"].Healthy, nodeStatus.Services["containerd"].Healthy, strings.Join(nodeStatus.Addresses, ","))
			fmt.Printf("%s\t%s\t%s\n", nodeStatus.Node, nodeStatus.WatcherState, strings.Join(nodeStatus.Addresses, ","))
		}
		fmt.Println()
	}
}
