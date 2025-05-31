package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"

	"github.com/DRuggeri/labwatch/wm"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	mgr := wm.NewWindowsManager(context.Background(), log)

	cmd := exec.Command("lxterminal", "--command", `bash -c 'LOKI_ADDR=https://boss.local:3100 logcli-linux-amd64 query --output=jsonl -f "{ host_name =~ \".+\" } | json "'`)
	mgr.StartWindow(context.Background(), cmd, wm.WMScreenBottom)
}
