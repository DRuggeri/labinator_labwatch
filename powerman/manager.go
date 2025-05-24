package powerman

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jacobsa/go-serial/serial"
)

type PowerStatus struct {
	P1 bool `json:"P1" yaml:"P1"`
	P2 bool `json:"P2" yaml:"P2"`
	P3 bool `json:"P3" yaml:"P3"`
	P4 bool `json:"P4" yaml:"P4"`
	P5 bool `json:"P5" yaml:"P5"`
	P6 bool `json:"P6" yaml:"P6"`
	P7 bool `json:"P7" yaml:"P7"`
	P8 bool `json:"P8" yaml:"P8"`
}

var sleepDuration = time.Second

type Port string

func (p Port) IsValid() bool {
	switch p {
	case "1", "2", "3", "4", "5", "6", "7", "8", "all":
		return true
	}
	return false
}

const (
	PALL Port = "all"
	P1   Port = "1"
	P2   Port = "2"
	P3   Port = "3"
	P4   Port = "4"
	P5   Port = "5"
	P6   Port = "6"
	P7   Port = "7"
	P8   Port = "8"
)

var ALL_PORTS = []Port{P1, P2, P3, P4, P5, P6}

const COMMAND_STATUS = "status\r\n"
const COMMAND_ON_TEMPLATE = "on %v\r\n"
const COMMAND_OFF_TEMPLATE = "off %v\r\n"

type PowerManager struct {
	opts    serial.OpenOptions
	port    io.ReadWriteCloser
	log     *slog.Logger
	status  PowerStatus
	running bool
}

func NewPowerManager(port string, l *slog.Logger) (*PowerManager, error) {
	opts := serial.OpenOptions{
		PortName:              port,
		BaudRate:              9600,
		DataBits:              8,
		ParityMode:            serial.PARITY_NONE,
		StopBits:              1,
		InterCharacterTimeout: 100,
		MinimumReadSize:       0,
	}

	// Open the port and get status to confirm functionality
	p, err := serial.Open(opts)
	if err != nil {
		return nil, err
	}

	m := &PowerManager{
		opts:    opts,
		port:    p,
		log:     l.With("operation", "powermanager"),
		running: true,
	}

	err = m.updateStatus()
	if err != nil {
		return nil, err
	}
	go m.internalWatch()

	return m, nil
}

func (m *PowerManager) Stop() {
	m.running = false
}

func (m *PowerManager) internalWatch() {
	for m.running {
		err := m.updateStatus()
		if err != nil {
			m.log.Error("failed to update status", "error", err.Error())
		}

		time.Sleep(sleepDuration)
	}
}

func (m *PowerManager) Watch(controlContext context.Context, resultChan chan<- PowerStatus) {
	curStatus := PowerStatus{}
	for {
		select {
		case <-controlContext.Done():
			return
		default:
			// Not ready to read from control channel - carry on
		}

		if m.status.P1 != curStatus.P1 || m.status.P2 != curStatus.P2 || m.status.P3 != curStatus.P3 || m.status.P4 != curStatus.P4 ||
			m.status.P5 != curStatus.P5 || m.status.P6 != curStatus.P6 || m.status.P7 != curStatus.P7 || m.status.P8 != curStatus.P8 {
			resultChan <- m.status
			curStatus = m.status
		}

		time.Sleep(sleepDuration)
	}
}

func (m *PowerManager) updateStatus() error {
	l, err := m.port.Write([]byte(COMMAND_STATUS))
	if err != nil {
		return err
	}
	if l < len(COMMAND_STATUS) {
		return fmt.Errorf("error getting status: expected to write %d but only wrote %d", len(COMMAND_STATUS), l)
	}

	s := bufio.NewScanner(m.port)

	// In case there are multiple lines, ignore all but the last
	line := ""
	for s.Scan() {
		line = s.Text()
	}
	if err := s.Err(); err != nil && len(line) == 0 {
		return err
	}

	res := make([]bool, 8)
	parts := strings.Split(line, " ")
	if len(parts) != 9 {
		m.log.Warn("incorrect status response detected", "line", line)
		return nil
	}

	for i := 1; i <= 8; i++ {
		if parts[i] == "1" {
			res[i-1] = true
		}
	}
	m.status = PowerStatus{
		P1: res[0],
		P2: res[1],
		P3: res[2],
		P4: res[3],
		P5: res[4],
		P6: res[5],
		P7: res[6],
		P8: res[7],
	}
	//m.log.Debug("status reset", "P1", m.status.P1, "P2", m.status.P2, "P3", m.status.P3, "P4", m.status.P4, "P5", m.status.P5, "P6", m.status.P6, "P7", m.status.P7, "P8", m.status.P8)
	return nil
}

func (m *PowerManager) GetStatus() PowerStatus {
	return m.status
}

func (m *PowerManager) PortIsOn(i Port) bool {
	// Ignore bunk inputs
	if !i.IsValid() {
		m.log.Warn("bunk port provided - ignoring", "port", i)
		return false
	}

	ports := []Port{i}
	if i == PALL {
		ports = ALL_PORTS
	}

	for _, p := range ports {
		if !m.GetPortStatus(p) {
			return false
		}
	}
	return true
}

func (m *PowerManager) PortIsOff(i Port) bool {
	// Ignore bunk inputs
	if !i.IsValid() {
		m.log.Warn("bunk port provided - ignoring", "port", i)
		return false
	}

	ports := []Port{i}
	if i == PALL {
		ports = ALL_PORTS
	}

	for _, p := range ports {
		if m.GetPortStatus(p) {
			return false
		}
	}
	return true
}

func (m *PowerManager) TurnOn(i Port) error {
	// Ignore bunk inputs
	if !i.IsValid() {
		m.log.Warn("bunk port provided - ignoring", "port", i)
		return nil
	}

	ports := []Port{i}
	if i == PALL {
		ports = ALL_PORTS
	}

	for _, p := range ports {
		m.log.Debug(fmt.Sprintf("turning on port %v", p), "current", m.GetPortStatus(p))

		if m.GetPortStatus(p) {
			// Already on
			return nil
		}

		cmd := fmt.Sprintf(COMMAND_ON_TEMPLATE, p)
		l, err := m.port.Write([]byte(cmd))
		if err != nil {
			return err
		}
		if l < len(cmd) {
			return fmt.Errorf("error turning on port: expected to write %d but only wrote %d", len(cmd), l)
		}
	}

	return nil
}

func (m *PowerManager) TurnOff(i Port) error {
	// Ignore bunk inputs
	if !i.IsValid() {
		m.log.Warn("bunk port provided - ignoring", "port", i)
		return nil
	}

	ports := []Port{i}
	if i == PALL {
		ports = ALL_PORTS
	}

	for _, p := range ports {
		m.log.Debug(fmt.Sprintf("turning off port %v", p), "current", m.GetPortStatus(p))

		if !m.GetPortStatus(p) {
			// Already off
			continue
		}

		cmd := fmt.Sprintf(COMMAND_OFF_TEMPLATE, p)
		l, err := m.port.Write([]byte(cmd))
		if err != nil {
			return err
		}
		if l < len(cmd) {
			return fmt.Errorf("error turning off port: expected to write %d but only wrote %d", len(cmd), l)
		}
	}

	return nil
}

func (m *PowerManager) Restart(i Port) error {
	// Ignore bunk inputs
	if !i.IsValid() {
		m.log.Warn("bunk port provided - ignoring", "port", i)
		return nil
	}

	ports := []Port{i}
	if i == PALL {
		ports = ALL_PORTS
	}

	m.log.Debug(fmt.Sprintf("restarting ports %v", ports))

	for _, p := range ports {
		// Only turn it off if it is on
		if m.GetPortStatus(p) {
			err := m.TurnOff(p)
			if err != nil {
				return fmt.Errorf("error restarting port %v: %w", p, err)
			}
		}
	}

	// Spin until the status is updated on all of them
	for _, p := range ports {
		for m.GetPortStatus(p) {
			time.Sleep(time.Millisecond * 10)
		}
	}

	for _, p := range ports {
		err := m.TurnOn(p)
		if err != nil {
			return err
		}
	}

	return nil
}

func (m *PowerManager) GetPortStatus(p Port) bool {
	switch p {
	case P1:
		return m.status.P1
	case P2:
		return m.status.P2
	case P3:
		return m.status.P3
	case P4:
		return m.status.P4
	case P5:
		return m.status.P5
	case P6:
		return m.status.P6
	case P7:
		return m.status.P7
	case P8:
		return m.status.P8
	default:
		return false
	}
}

func (m *PowerManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	action := r.URL.Query().Get("action")
	port := r.URL.Query().Get("port")

	var p Port
	switch strings.ToLower(port) {
	case "p1":
		p = P1
	case "p2":
		p = P2
	case "p3":
		p = P3
	case "p4":
		p = P4
	case "p5":
		p = P5
	case "p6":
		p = P6
	case "p7":
		p = P7
	case "p8":
		p = P8
	case "all":
		p = PALL
	default:
		m.log.Info("bad request for port", "port", port)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var err error
	switch strings.ToLower(action) {
	case "turnon":
		m.log.Info("powerman action", "action", action, "port", port)
		err = m.TurnOn(p)
	case "turnoff":
		m.log.Info("powerman action", "action", action, "port", port)
		err = m.TurnOff(p)
	case "restart":
		m.log.Info("powerman action", "action", action, "port", port)
		err = m.Restart(p)
	default:
		m.log.Info("bad request for action", "action", action)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	m.log.Error(fmt.Sprintf("failed to %s port %s", action, port), "error", err.Error())
	w.WriteHeader(http.StatusInternalServerError)
}
