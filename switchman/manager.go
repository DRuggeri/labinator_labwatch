package switchman

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const USERNAME = "admin"
const PASSWORD = "admin"

type SwitchStatus struct {
	Port1 bool `json:"Port1" yaml:"Port1"`
	Port2 bool `json:"Port2" yaml:"Port2"`
	Port3 bool `json:"Port3" yaml:"Port3"`
	Port4 bool `json:"Port4" yaml:"Port4"`
	Port5 bool `json:"Port5" yaml:"Port5"`
	Port6 bool `json:"Port6" yaml:"Port6"`
	Port7 bool `json:"Port7" yaml:"Port7"`
	Port8 bool `json:"Port8" yaml:"Port8"`
}

type SwitchPort string

func (p SwitchPort) IsValid() bool {
	switch p {
	case "1", "2", "3", "4", "5", "6", "7", "8", "all":
		return true
	}
	return false
}

const (
	SPALL   SwitchPort = "all"
	SPNODES SwitchPort = "nodes"
	SP1     SwitchPort = "1"
	SP2     SwitchPort = "2"
	SP3     SwitchPort = "3"
	SP4     SwitchPort = "4"
	SP5     SwitchPort = "5"
	SP6     SwitchPort = "6"
	SP7     SwitchPort = "7"
	SP8     SwitchPort = "8"
)

var ALL_SWITCH_PORTS = []SwitchPort{SP1, SP2, SP3, SP4, SP5, SP6, SP7, SP8}
var NODES_SWITCH_PORTS = []SwitchPort{SP6, SP5, SP4, SP3, SP1, SP2}

type SwitchManager struct {
	baseURL   string
	client    *http.Client
	log       *slog.Logger
	ts        time.Time
	status    SwitchStatus
	running   bool
	cookie    string
	cookieJar *cookiejar.Jar
}

func NewSwitchManager(baseURL string, l *slog.Logger) (*SwitchManager, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		Jar:     jar,
	}

	m := &SwitchManager{
		baseURL:   baseURL,
		client:    client,
		log:       l.With("operation", "switchmanager"),
		running:   true,
		cookieJar: jar,
	}

	// Attempt to login and get initial status
	err = m.login()
	if err != nil {
		return nil, fmt.Errorf("failed to login: %w", err)
	}

	err = m.updateStatus()
	if err != nil {
		return nil, fmt.Errorf("failed to get initial status: %w", err)
	}

	go m.internalWatch()

	return m, nil
}

func (m *SwitchManager) Stop() {
	m.running = false
}

func (m *SwitchManager) internalWatch() {
	for m.running {
		time.Sleep(5 * time.Second)
		err := m.updateStatus()
		if err != nil {
			m.log.Error("Failed to update status", "error", err)
		}
	}
}

func (m *SwitchManager) Watch(controlContext context.Context, resultChan chan<- SwitchStatus) {
	defer close(resultChan)

	lastResult := m.status
	resultChan <- lastResult

	for m.running {
		select {
		case <-controlContext.Done():
			m.log.Info("Control context cancelled - stopping watch")
			return
		default:
			time.Sleep(1 * time.Second)

			if m.status != lastResult {
				resultChan <- m.status
				lastResult = m.status
			}
		}
	}
}

// login attempts to authenticate with the switch using MD5 hash
func (m *SwitchManager) login() error {
	loginURL := m.baseURL + "/login.cgi"

	// Calculate MD5 hash of username + password
	hash := md5.Sum([]byte(USERNAME + PASSWORD))
	response := fmt.Sprintf("%x", hash)

	// Set the admin cookie
	adminCookie := &http.Cookie{
		Name:  "admin",
		Value: response,
		Path:  "/",
	}

	// Parse URL for cookie jar
	u, err := url.Parse(m.baseURL)
	if err != nil {
		return fmt.Errorf("failed to parse base URL: %w", err)
	}

	// Add cookie to jar
	m.cookieJar.SetCookies(u, []*http.Cookie{adminCookie})
	m.cookie = response

	// Prepare form data
	formData := url.Values{}
	formData.Set("username", USERNAME)
	formData.Set("password", PASSWORD)
	formData.Set("language", "EN")
	formData.Set("Response", response)

	// Create request with proper headers
	req, err := http.NewRequest("POST", loginURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create login request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)

	// Submit login
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to submit login: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("login failed with status code: %d", resp.StatusCode)
	}

	m.log.Info("Login successful", "cookie", m.cookie)
	return nil
}

// updateStatus fetches the current switch status from the port.cgi page
func (m *SwitchManager) updateStatus() error {
	portURL := m.baseURL + "/port.cgi"

	resp, err := m.client.Get(portURL)
	if err != nil {
		return fmt.Errorf("failed to get port status: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read port status: %w", err)
	}

	// Parse the HTML to extract port statuses
	newStatus, err := m.parsePortStatus(string(body))
	if err != nil {
		return fmt.Errorf("failed to parse port status: %w", err)
	}

	m.status = newStatus
	m.ts = time.Now()

	return nil
}

// GetStatus returns the current switch status
func (m *SwitchManager) GetStatus() SwitchStatus {
	return m.status
}

// PortIsOn checks if a specific port is enabled
func (m *SwitchManager) PortIsOn(port SwitchPort) bool {
	switch port {
	case SP1:
		return m.status.Port1
	case SP2:
		return m.status.Port2
	case SP3:
		return m.status.Port3
	case SP4:
		return m.status.Port4
	case SP5:
		return m.status.Port5
	case SP6:
		return m.status.Port6
	case SP7:
		return m.status.Port7
	case SP8:
		return m.status.Port8
	}
	return false
}

// PortIsOff checks if a specific port is disabled
func (m *SwitchManager) PortIsOff(port SwitchPort) bool {
	return !m.PortIsOn(port)
}

// TurnOn enables a specific port
func (m *SwitchManager) TurnOn(ports ...SwitchPort) error {
	if len(ports) == 1 {
		switch ports[0] {
		case SPALL:
			ports = ALL_SWITCH_PORTS
		case SPNODES:
			ports = NODES_SWITCH_PORTS
		}
	}

	for _, port := range ports {
		if !port.IsValid() {
			return fmt.Errorf("invalid port: %s", port)
		}

		if m.PortIsOn(port) {
			continue
		}

		err := m.controlPort(port, true)
		if err != nil {
			return fmt.Errorf("failed to turn on port %s: %w", port, err)
		}
	}
	return nil
}

// TurnOff disables a specific port
func (m *SwitchManager) TurnOff(ports ...SwitchPort) error {
	if len(ports) == 1 {
		switch ports[0] {
		case SPALL:
			ports = ALL_SWITCH_PORTS
		case SPNODES:
			ports = NODES_SWITCH_PORTS
		}
	}

	for _, port := range ports {
		if !port.IsValid() {
			return fmt.Errorf("invalid port: %s", port)
		}

		if m.PortIsOff(port) {
			continue
		}

		err := m.controlPort(port, false)
		if err != nil {
			return fmt.Errorf("failed to turn off port %s: %w", port, err)
		}
	}

	return nil
}

// controlPort sends the HTTP request to enable/disable a port
func (m *SwitchManager) controlPort(port SwitchPort, enable bool) error {
	portURL := m.baseURL + "/port.cgi"

	// Convert 1-based port to 0-based portid
	portid, err := m.portToPortID(port)
	if err != nil {
		return fmt.Errorf("failed to convert port to portid: %w", err)
	}

	// Prepare form data
	formData := url.Values{}
	formData.Set("portid", fmt.Sprintf("%d", portid))
	formData.Set("name", "")
	if enable {
		formData.Set("state", "1") // 1 = enabled
	} else {
		formData.Set("state", "0") // 0 = disabled
	}
	formData.Set("speed_duplex", "0") // Auto
	formData.Set("flow", "0")         // Off
	formData.Set("submit", "   Apply   ")
	formData.Set("cmd", "port")

	// Create request with proper headers and cookie
	req, err := http.NewRequest("POST", portURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create port control request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Add the admin cookie
	if m.cookie != "" {
		adminCookie := &http.Cookie{
			Name:  "admin",
			Value: m.cookie,
			Path:  "/",
		}
		req.AddCookie(adminCookie)
	}

	// Submit the request
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to submit port control: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("port control failed with status code: %d", resp.StatusCode)
	}

	action := "disable"
	if enable {
		action = "enable"
	}
	m.log.Info("Port control successful", "port", port, "action", action)

	// Update status after a short delay to allow switch to process the change
	time.Sleep(500 * time.Millisecond)
	err = m.updateStatus()
	if err != nil {
		m.log.Warn("Failed to update status after port control", "error", err)
	}

	return nil
}

// portToPortID converts a 1-based SwitchPort to 0-based portid
func (m *SwitchManager) portToPortID(port SwitchPort) (int, error) {
	switch port {
	case SP1:
		return 0, nil
	case SP2:
		return 1, nil
	case SP3:
		return 2, nil
	case SP4:
		return 3, nil
	case SP5:
		return 4, nil
	case SP6:
		return 5, nil
	case SP7:
		return 6, nil
	case SP8:
		return 7, nil
	default:
		return -1, fmt.Errorf("invalid port: %s", port)
	}
}

func (m *SwitchManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	action := r.URL.Query().Get("action")
	port := r.URL.Query().Get("port")

	var p []SwitchPort
	switch strings.ToLower(port) {
	case "p1":
		p = []SwitchPort{SP1}
	case "p2":
		p = []SwitchPort{SP2}
	case "p3":
		p = []SwitchPort{SP3}
	case "p4":
		p = []SwitchPort{SP4}
	case "p5":
		p = []SwitchPort{SP5}
	case "p6":
		p = []SwitchPort{SP6}
	case "p7":
		p = []SwitchPort{SP7}
	case "p8":
		p = []SwitchPort{SP8}
	case "all":
		p = []SwitchPort{SP6, SP5, SP4, SP3, SP1, SP2}
	case "group1":
		p = []SwitchPort{SP6, SP5}
	case "group2":
		p = []SwitchPort{SP4, SP3}
	case "group3":
		p = []SwitchPort{SP1, SP2}
	default:
		m.log.Info("bad request for port", "port", port)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var err error
	switch strings.ToLower(action) {
	case "enable":
		m.log.Info("switchman action", "action", action, "port", port)
		err = m.TurnOn(p...)
	case "disable":
		m.log.Info("switchman action", "action", action, "port", port)
		err = m.TurnOff(p...)
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
