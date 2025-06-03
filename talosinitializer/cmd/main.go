package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/DRuggeri/labwatch/lablinkmanager"
	"github.com/DRuggeri/labwatch/powerman"
	"github.com/DRuggeri/labwatch/talosinitializer"
	"github.com/DRuggeri/labwatch/watchers/port"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, tmp := context.WithCancel(context.Background())
	defer tmp()

	pMan, err := powerman.NewPowerManager("/dev/serial/by-id/usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0", log)
	if err != nil {
		log.Error("failed to create the power manager", "error", err.Error())
		os.Exit(1)
	}

	// Give pMan time to catch status
	time.Sleep(2 * time.Second)

	activeLab := "virtual-2"
	initializer, err := talosinitializer.NewTalosInitializer("/home/boss/.talos/config", "/home/boss/talos/scenarios/configs.yaml", "/home/boss/talos/scenarios", "koob", log)
	if err != nil {
		panic(err)
	}

	activeEndpoints, err := initializer.GetWatchEndpoints(activeLab)
	if err != nil {
		panic(err)
	}
	if len(activeEndpoints) < 1 {
		panic("there are no active endpoints we are watching!")
	}

	log.Info("configuring lab", "lab", activeLab)
	labMan := lablinkmanager.NewLinkManager("/var/www/html/nodes-ipxe/", "lab", activeLab)
	labMan.EnableLab()

	// Ensure boxes are down
	pMan.TurnOff(powerman.PALL)
	// Wait for all to be off
	for {
		if pMan.PortIsOff(powerman.PALL) {
			break
		}
		time.Sleep(time.Millisecond * 100)
	}

	portWatcher, err := port.NewPortWatcher(ctx, activeEndpoints, false, log)
	if err != nil {
		panic(err)
	}
	portInfo := make(chan port.PortStatus)
	go portWatcher.Watch(ctx, portInfo)

	portBroadcast := initializer.GetPortChan()
	iInfo := initializer.GetStatusUpdateChan()

	initializerCtx, initializerCancel := context.WithCancel(ctx)
	defer initializerCancel()
	go initializer.Initialize(initializerCtx, activeLab, labMan, pMan)

OUTER:
	for {
		select {
		case <-ctx.Done():
			break OUTER
		case i, ok := <-iInfo:
			if ok {
				log.Debug("received initializer status update", "step", i.CurrentStep)
			} else {
				log.Error("error encountered reading initializer status")
			}
		case p, ok := <-portInfo:
			if ok {
				log.Debug("received port info update", "status", p)
				if portBroadcast != nil {
					portBroadcast <- p
				} else {
					log.Debug("discarding update since no portBroadcast is configured")
				}
			} else {
				log.Error("error encountered reading port states")
			}
		default:
			time.Sleep(time.Millisecond * 100)
			continue
		}
	}

	for {
		time.Sleep(time.Second)
	}
}
