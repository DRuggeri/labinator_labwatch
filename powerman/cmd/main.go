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
