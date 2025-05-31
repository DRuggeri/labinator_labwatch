package wm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"time"

	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgbutil"
	"github.com/BurntSushi/xgbutil/ewmh"
	"github.com/BurntSushi/xgbutil/icccm"
	"github.com/BurntSushi/xgbutil/xrect"
	"github.com/BurntSushi/xgbutil/xwindow"
)

type ProcManager struct {
	bottomScreen xrect.Rect
	cmds         map[ManagedWindowScreen]*exec.Cmd
	x            *xgbutil.XUtil
	log          slog.Logger
}

type ManagedWindowScreen int

const WMScreenTop = 1
const WMScreenBottom = 2

// TODO - await start of x session

func NewWindowsManager(ctx context.Context, log *slog.Logger) *ProcManager {
	if os.Getenv("DISPLAY") == "" {
		os.Setenv("DISPLAY", ":0")
	}

	m := &ProcManager{
		log:  *log.With("operation", "WindowsManager"),
		cmds: make(map[ManagedWindowScreen]*exec.Cmd),
	}

	// If the X server isn't up yet or not available, try until it is
	go func() {
		for {
			if err := m.getConnection(); err != nil {
				time.Sleep(time.Second)
				continue
			}
			for {
				e, err := m.x.Conn().WaitForEvent()
				if e == nil && err == nil {
					// connection closed when both are nil - break out of
					// the inner loop and enter reconnect loop
					m.x = nil
					break
				}
			}
		}
	}()

	return m
}

func (m *ProcManager) StartWindow(ctx context.Context, cmd *exec.Cmd, s ManagedWindowScreen) error {
	m.awaitConnection()

	m.Kill(s)

	beforeStartClientIds, err := ewmh.ClientListGet(m.x)
	if err != nil {
		return fmt.Errorf("failed to get client list: %w", err)
	}

	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}
	m.cmds[s] = cmd

	var newId xproto.Window
	// Wait for window to open
	for {
		ids, err := ewmh.ClientListGet(m.x)
		if err != nil {
			return fmt.Errorf("failed to get client list during loop: %w", err)
		}

		for _, id := range ids {
			if !slices.Contains(beforeStartClientIds, id) {
				newId = id
				break
			}
		}

		if newId == 0 {
			time.Sleep(time.Millisecond * 100)
		} else {
			break
		}
	}

	// Move and fullscreen our window
	m.log.Debug("new id detected", "id", newId)
	win := xwindow.New(m.x, newId)
	ewmh.WmStateReq(m.x, newId, ewmh.StateRemove, "_NET_WM_STATE_FULLSCREEN")
	win.MoveResize(m.bottomScreen.X(), m.bottomScreen.Y(), m.bottomScreen.Width(), m.bottomScreen.Height())
	ewmh.WmStateReq(m.x, newId, ewmh.StateAdd, "_NET_WM_STATE_FULLSCREEN")

	return nil
}

func (m *ProcManager) Kill(s ManagedWindowScreen) {
	if m.cmds[s] != nil {
		m.log.Debug("existing process is running", "pid", m.cmds[s].Process.Pid)
		m.cmds[s].Process.Signal(syscall.SIGTERM)
		m.cmds[s].Wait()
		m.cmds[s] = nil
	}
}

func (m *ProcManager) awaitConnection() {
	for m.x == nil {
		time.Sleep(time.Millisecond * 100)
	}
}

func (m *ProcManager) getConnection() error {
	if m.x != nil {
		return nil
	}
	x, err := xgbutil.NewConn()
	if err != nil {
		return err
	}
	m.log.Info("connected to X session")
	m.x = x

	// This is probably a labinator-specific quick hack, but find the geometries of
	// our screens since xgbutil doesn't offer such a built-in
	ids, err := ewmh.ClientListGet(m.x)
	if err != nil {
		return fmt.Errorf("failed to get client list after getting connection: %w", err)
	}
	for _, id := range ids {
		name, _ := icccm.WmNameGet(x, id)
		win := xwindow.New(x, id)
		geom, _ := win.DecorGeometry()

		if name == "pcmanfm" {
			if geom.Y() != 0 {
				m.bottomScreen = geom
			}
		}
	}

	return nil
}
