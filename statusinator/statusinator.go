package statusinator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/DRuggeri/labwatch/watchers/loki"
	"github.com/jacobsa/go-serial/serial"
)

type Statusinator struct {
	opts serial.OpenOptions
	port io.ReadWriteCloser
	log  *slog.Logger
	mux  *sync.Mutex
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

	return m, nil
}

func (m *Statusinator) Watch(controlContext context.Context, status <-chan LabStatus, events <-chan loki.LogEvent) {
	m.log.Info("watching for statuses")
	for {
		select {
		case <-controlContext.Done():
			return
		case s, ok := <-status:
			if ok {
				m.log.Debug("received status update")
				b, err := json.Marshal(s)
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
		}
	}
}

func (m *Statusinator) send(t string, payload []byte) {
	m.mux.Lock()
	defer m.mux.Unlock()

	b := []byte(t)
	b = append(b, ':')
	b = append(b, payload...)
	b = append(b, '\n')

	m.port.Write(b)
}
