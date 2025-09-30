package routerman

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// WifiNetwork represents a WiFi network from OpenWRT
type WifiNetwork struct {
	SSID    string `json:"ssid"`
	Secured bool   `json:"secured"`
	Signal  int    `json:"signal"`
	BSSID   string `json:"bssid,omitempty"`
}

// RouterManager manages OpenWRT router WiFi operations via JSON-RPC
type RouterManager struct {
	baseURL      string
	client       *http.Client
	log          *slog.Logger
	sessionID    string
	username     string
	password     string
	requestID    int
	networkCache map[string]WiFiScanResult
}

// NewRouterManager creates a new RouterManager instance
func NewRouterManager(baseURL string, username string, password string, l *slog.Logger) (*RouterManager, error) {
	// Create HTTP client with TLS verification disabled
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	m := &RouterManager{
		baseURL:      baseURL,
		client:       client,
		log:          l.With("operation", "routermanager"),
		username:     username,
		password:     password,
		requestID:    1,
		networkCache: make(map[string]WiFiScanResult),
	}

	err := m.authenticate()
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate: %w", err)
	}

	return m, nil
}

// authenticate performs authentication with OpenWRT LuCI RPC interface
func (m *RouterManager) authenticate() error {
	authParams := []interface{}{m.username, m.password}

	response, err := m.makeRPCCall("auth", "login", authParams, false)
	if err != nil {
		return fmt.Errorf("authentication request failed: %w", err)
	}

	m.sessionID = response.Result
	m.log.Info("Authentication successful", "sessionID", m.sessionID)

	return nil
}

// GetWifiNetworks retrieves available WiFi networks via LuCI RPC
func (m *RouterManager) GetWifiNetworks(ctx context.Context) ([]WifiNetwork, error) {
	scanParams := []interface{}{
		`ubus call iwinfo scan {\"device\":\"radio0\"}`,
	}

	scanResponse, err := m.makeRPCCall("sys", "exec", scanParams, true)
	if err != nil {
		return nil, fmt.Errorf("scan request failed: %w", err)
	}

	var results WiFiScanResponse
	err = json.Unmarshal([]byte(scanResponse.Result), &results)
	if err != nil {
		return nil, fmt.Errorf("failed parsing scan response: %w", err)
	}

	// Convert to our WiFi network structure
	var networks []WifiNetwork
	for _, item := range results.Results {
		network := WifiNetwork{
			SSID:    item.SSID,
			Secured: item.Encryption.Enabled,
			Signal:  item.Signal,
			BSSID:   item.BSSID,
		}
		networks = append(networks, network)
		if item.SSID != "" {
			m.networkCache[item.SSID] = item
		}
	}

	m.log.Debug("Retrieved WiFi networks", "count", len(networks))
	return networks, nil
}

// ConnectToWifiNetwork connects to a WiFi network via JSON-RPC
func (m *RouterManager) ConnectToWifiNetwork(ctx context.Context, ssid string, password string) (*ConnectResult, error) {
	m.log.Info("Attempting to connect to WiFi network", "ssid", ssid)

	// TODO

	return nil, fmt.Errorf("not implemented")
}

// makeRPCCall tickles the RPC API
func (m *RouterManager) makeRPCCall(endpoint, method string, params []interface{}, useAuth bool) (*JsonRpcResponse, error) {
	m.requestID++

	// Prepare JSON-RPC request
	request := JsonRpcRequest{
		JsonRpc: "2.0",
		ID:      m.requestID,
		Method:  method,
		Params:  params,
	}

	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON-RPC request: %w", err)
	}

	m.log.Debug("Making LuCI RPC call", "endpoint", endpoint, "method", method, "requestID", m.requestID)

	// Create HTTP request
	url := fmt.Sprintf("%s/cgi-bin/luci/rpc/%s", m.baseURL, endpoint)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Add authentication if required
	if useAuth && m.sessionID != "" {
		req.Header.Set("Cookie", fmt.Sprintf("sysauth=%s", m.sessionID))
	}

	// Make the request
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	m.log.Debug("Raw LuCI response", "body", string(responseBody))

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP request failed with status %d: %s", resp.StatusCode, string(responseBody))
	}

	// Parse JSON-RPC response
	var jsonRpcResponse JsonRpcResponse
	err = json.Unmarshal(responseBody, &jsonRpcResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JSON-RPC response: %w", err)
	}

	// Check for JSON-RPC errors
	if jsonRpcResponse.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error %d: %s",
			jsonRpcResponse.Error.Code,
			jsonRpcResponse.Error.Message)
	}

	return &jsonRpcResponse, nil
}
