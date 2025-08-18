package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/DRuggeri/labwatch/watchers/kubernetes"
	"gopkg.in/yaml.v3"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	w, err := kubernetes.NewKubeWatcher("", "default", log)
	if err != nil {
		panic(err)
	}

	info := make(chan map[string]kubernetes.PodStatus)
	go w.Watch(context.Background(), info)
	for {
		podInfo := <-info
		d, _ := yaml.Marshal(podInfo)
		fmt.Printf("%s\n", d)
	}
}
