package statusinator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"

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

				// Create a deep copy of everything to prevent concurrent modification
				briefStatus := BriefStatus{
					Power: powerman.PowerStatus{
						P1: s.Power.P1,
						P2: s.Power.P2,
						P3: s.Power.P3,
						P4: s.Power.P4,
						P5: s.Power.P5,
						P6: s.Power.P6,
						P7: s.Power.P7,
						P8: s.Power.P8,
					},
					Logs: common.LogStats{
						NumMessages:          s.Logs.NumMessages,
						NumEmergencyMessages: s.Logs.NumEmergencyMessages,
						NumAlertMessages:     s.Logs.NumAlertMessages,
						NumCriticalMessages:  s.Logs.NumCriticalMessages,
						NumErrorMessages:     s.Logs.NumErrorMessages,
						NumWarnMessages:      s.Logs.NumWarnMessages,
						NumNoticeMessages:    s.Logs.NumNoticeMessages,
						NumInfoMessages:      s.Logs.NumInfoMessages,
						NumDebugMessages:     s.Logs.NumDebugMessages,
						NumDHCPDiscover:      s.Logs.NumDHCPDiscover,
						NumDHCPLeased:        s.Logs.NumDHCPLeased,
					},
					Initializer: talosinitializer.InitializerStatus{
						LabName:                s.Initializer.LabName,
						NumHypervisors:         s.Initializer.NumHypervisors,
						InitializedHypervisors: s.Initializer.InitializedHypervisors,
						NumNodes:               s.Initializer.NumNodes,
						InitializedNodes:       s.Initializer.InitializedNodes,
						NumPods:                s.Initializer.NumPods,
						InitializedPods:        s.Initializer.InitializedPods,
						CurrentStep:            s.Initializer.CurrentStep,
						Failed:                 s.Initializer.Failed,
						TimeSpent:              make(map[string]int),
					},
				}

				// Ignore the TimeSpent map
				/*
					for k, v := range s.Initializer.TimeSpent {
						briefStatus.Initializer.TimeSpent[k] = v
					}
				*/

				b, err := json.Marshal(briefStatus)
				if err != nil {
					m.log.Error("failed to marshal to JSON", "error", err)
					return
				}
				m.send("status", b)
			} else {
				m.log.Error("error encountered reading log stats")
			}
		case e, ok := <-events:
			if ok {
				m.log.Debug("received log event")
				if e.Attributes == nil {
					e.Attributes = make(map[string]string)
				}
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
