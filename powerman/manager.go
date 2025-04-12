package powerman

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"strings"

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

type Port int

func (p Port) IsValid() bool {
	if p < 1 || p > 8 {
		return false
	}
	return true
}

const (
	_ Port = iota
	P1
	P2
	P3
	P4
	P5
	P6
	P7
	P8
)

const COMMAND_STATUS = "status\r\n"
const COMMAND_ON_TEMPLATE = "on %d\r\n"
const COMMAND_OFF_TEMPLATE = "off %d\r\n"

type PowerManager struct {
	opts serial.OpenOptions
	port io.ReadWriteCloser
	log  *slog.Logger
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
		opts: opts,
		port: p,
		log:  l.With("operation", "powermanager"),
	}

	_, err = m.GetStatus()
	if err != nil {
		return nil, err
	}

	return m, nil
}

func (m *PowerManager) GetStatus() (PowerStatus, error) {
	l, err := m.port.Write([]byte(COMMAND_STATUS))
	if err != nil {
		return PowerStatus{}, err
	}
	if l < len(COMMAND_STATUS) {
		return PowerStatus{}, fmt.Errorf("error getting status: expected to write %d but only wrote %d", len(COMMAND_STATUS), l)
	}

	s := bufio.NewScanner(m.port)

	// In case there are multiple lines, ignore all but the last
	line := ""
	for s.Scan() {
		line = s.Text()
	}
	if err := s.Err(); err != nil && len(line) == 0 {
		return PowerStatus{}, err
	}

	res := make([]bool, 8)
	parts := strings.Split(line, " ")
	for i := 1; i <= 8; i++ {
		if parts[i] == "1" {
			res[i-1] = true
		}
	}
	return PowerStatus{
		P1: res[0],
		P2: res[1],
		P3: res[2],
		P4: res[3],
		P5: res[4],
		P6: res[5],
		P7: res[6],
		P8: res[7],
	}, nil
}

func (m *PowerManager) TurnOn(p Port) error {
	// Ignore bunk inputs
	if !p.IsValid() {
		m.log.Warn("bunk port provided - ignoring", "port", p)
		return nil
	}

	cmd := fmt.Sprintf(COMMAND_ON_TEMPLATE, p)
	l, err := m.port.Write([]byte(cmd))
	if err != nil {
		return err
	}
	if l < len(cmd) {
		return fmt.Errorf("error getting status: expected to write %d but only wrote %d", len(cmd), l)
	}

	return nil
}

func (m *PowerManager) TurnOff(p Port) error {
	// Ignore bunk inputs
	if !p.IsValid() {
		m.log.Warn("bunk port provided - ignoring", "port", p)
		return nil
	}

	cmd := fmt.Sprintf(COMMAND_OFF_TEMPLATE, p)
	l, err := m.port.Write([]byte(cmd))
	if err != nil {
		return err
	}
	if l < len(cmd) {
		return fmt.Errorf("error getting status: expected to write %d but only wrote %d", len(cmd), l)
	}

	return nil
}
