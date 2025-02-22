package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/DRuggeri/labwatch/watchers/talos"
)

func main() {
	w, err := talos.NewTalosWatcher(context.Background(), "/root/talos/talosconfig", "koobs")
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
