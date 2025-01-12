package talos

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/DRuggeri/labwatch/watchers"
	tclient "github.com/siderolabs/talos/pkg/machinery/client"
	tcconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
)

type TalosWatcher struct {
	Config       *tcconfig.Config
	Client       *tclient.Client
	TalosContext *tcconfig.Context
}

// func NewTalosWatcher(configFile string, clusterName string) (watchers.Watcher, error) {
func NewTalosWatcher(configFile string, clusterName string) (*TalosWatcher, error) {
	cfg, err := tcconfig.Open(configFile)
	if err != nil {
		return nil, err
	}

	var tctx *tcconfig.Context
	var ok bool
	if tctx, ok = cfg.Contexts[clusterName]; !ok {
		return nil, fmt.Errorf("the %s context name does not exist in the config file %s", clusterName, configFile)
	}

	if len(tctx.Nodes) == 0 {
		return nil, fmt.Errorf("there are no nodes defined in the %s config file", configFile)
	}

	client, err := tclient.New(context.Background(), tclient.WithConfig(cfg))
	if err != nil {
		return nil, err
	}

	return &TalosWatcher{
		Config:       cfg,
		Client:       client,
		TalosContext: tctx,
	}, err
}

func (w *TalosWatcher) TestIt() {
	fmt.Printf("Nodes:\n")
	for i, ip := range w.TalosContext.Nodes {
		nodename := ip
		if res, err := net.LookupAddr(ip); err == nil {
			nodename = strings.TrimSuffix(res[0], ".local.")
		}

		fmt.Printf("%d: %s (%s)\n", i, ip, nodename)
	}
	panic("oop")
}

func (w *TalosWatcher) Watch(control <-chan watchers.ControlAction, result chan<- watchers.WatchState) {
	status := watchers.ActionStart
	sleepDuration := time.Duration(1) * time.Second
	states := map[string]string{
		"node":  "down",
		"talos": "down",
	}

	for {
		select {
		case action, ok := <-control:
			if ok {
				status = action
				switch action {
				case watchers.ActionPause:
					sleepDuration = time.Duration(1) * time.Second
				case watchers.ActionStop:
					close(result)
					return
				}
			} else {
				// Control channel closed - wrap it up
				close(result)
				return
			}
		default:
		}

		if status == watchers.ActionPause {
			time.Sleep(sleepDuration)
			continue
		}

		result <- watchers.WatchState{
			States: states,
			Node:   "foo",
		}

		time.Sleep(sleepDuration)
	}
}
