package wm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"

	"github.com/BurntSushi/xgbutil"
)

type ProcManager struct {
	topWindow         *ManagedWindow
	bottomWindow      *ManagedWindow
	backgroundWindows *ManagedWindow
	x                 *xgbutil.XUtil
	log               slog.Logger
}

type ManagedWindow struct {
	pid int
}

type ManagedWindowScreen int

const WMScreenBackground = 1
const WMScreenTop = 2
const WMScreenBottom = 3

func NewWindowsManager(ctx context.Context, log slog.Logger) (*ProcManager, error) {
	if os.Getenv("DISPLAY") == "" {
		return nil, fmt.Errorf("DISPLAY environment variable not set")
	}

	return &ProcManager{
		log: *log.With("operation", "WindowsManager"),
	}, nil
}

func (m *ProcManager) StartWindow(ctx context.Context, cmd *exec.Cmd, s ManagedWindowScreen) error {
	if err := m.getConnection(); err != nil {
		return nil
	}

	return nil
}

func (m *ProcManager) getConnection() error {
	if m.x != nil {
		return nil
	}
	x, err := xgbutil.NewConn()
	if err != nil {
		return err
	}
	m.x = x
	return nil
}
