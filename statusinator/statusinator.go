package statusinator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/DRuggeri/labwatch/powerman"
	"github.com/DRuggeri/labwatch/talosinitializer"
	"github.com/DRuggeri/labwatch/watchers/common"
	"github.com/jacobsa/go-serial/serial"
)

type Statusinator struct {
	opts    serial.OpenOptions
	port    io.ReadWriteCloser
	log     *slog.Logger
	running bool
	mux     *sync.Mutex
}

// Cuts out Talos and port monitoring information to keep payload less than 2048 bytes
type BriefStatus struct {
	Initializer talosinitializer.InitializerStatus `json:"initializer"`
	Power       powerman.PowerStatus               `json:"power"`
	Logs        common.LogStats                    `json:"logs"`
}

func NewStatusinator(port string, l *slog.Logger) (*Statusinator, error) {
	opts := serial.OpenOptions{
		PortName:              port,
		BaudRate:              115200,
		DataBits:              8,
		ParityMode:            serial.PARITY_NONE,
		StopBits:              1,
		InterCharacterTimeout: 100,
		MinimumReadSize:       0,
	}

	p, err := serial.Open(opts)
	if err != nil {
		return nil, err
	}

	m := &Statusinator{
		opts: opts,
		port: p,
		log:  l.With("operation", "statusinator"),
		mux:  &sync.Mutex{},
	}

	m.send("boss", []byte("ready"))

	return m, nil
}

func (m *Statusinator) Watch(controlContext context.Context, status <-chan common.LabStatus, events <-chan common.LogEvent) {
	m.running = true
	m.log.Info("watching for statuses")
	for {
		select {
		case <-controlContext.Done():
			m.send("boss", []byte("shutdown"))
			m.running = false
			return
		case s, ok := <-status:
			if ok {
				m.log.Debug("received status update")
				b, err := json.Marshal(BriefStatus{
					Power:       s.Power,
					Logs:        s.Logs,
					Initializer: s.Initializer,
				})
				if err != nil {
					m.log.Error("failed to marshal to JSON", "object", b)
					return
				}
				m.send("status", b)
			} else {
				m.log.Error("error encountered reading log stats")
			}
		case e, ok := <-events:
			if ok {
				m.log.Debug("received log event")
				payload := fmt.Sprintf("%s [%s] %s: %s", e.Node, e.Level, e.Service, e.Message)
				if len(payload) > 80 {
					payload = payload[:80]
				}
				m.send("log", []byte(payload))
			} else {
				m.log.Error("error encountered reading events")
			}
		default:
			time.Sleep(time.Millisecond * 100)
		}
	}
}

func (m *Statusinator) Running() bool {
	return m.running
}

func (m *Statusinator) send(t string, payload []byte) {
	m.mux.Lock()
	defer m.mux.Unlock()

	b := []byte(t)
	b = append(b, ':')
	b = append(b, payload...)
	b = append(b, '\n')

	_, err := m.port.Write(b)
	if err != nil {
		m.port.Close()
		p, oerr := serial.Open(m.opts)
		if oerr != nil {
			m.log.Warn("error writing to port - failed to reopen", "error", err, "openError", oerr)
			return
		}
		m.log.Warn("error writing to port - reopened", "error", err)
		m.port = p
	}
}
