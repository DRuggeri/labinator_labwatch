package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/DRuggeri/labwatch/powerman"
)

func main() {
	lvl := slog.LevelVar{}
	lvl.Set(slog.LevelDebug)
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	pman, err := powerman.NewPowerManager("/dev/serial/by-id/usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0", log)
	if err != nil {
		panic(err)
	}

	// Port 8 is free - play around with it
	log.Info("turning on port 8")
	err = pman.TurnOn(powerman.P8)
	if err != nil {
		panic(err)
	}

	time.Sleep(time.Second)

	log.Info("turning off port 8")
	err = pman.TurnOff(powerman.P8)
	if err != nil {
		panic(err)
	}

	for {
		status, err := pman.GetStatus()
		if err != nil {
			panic(err)
		}
		b, _ := json.MarshalIndent(status, "", "  ")
		log.Info(string(b))
		time.Sleep(time.Second * 5)
	}
}
