package routerman

// ConnectResult represents the result of a WiFi connection attempt
type ConnectResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

// JsonRpcRequest represents a JSON-RPC request structure
type JsonRpcRequest struct {
	JsonRpc string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// JsonRpcResponse represents a JSON-RPC response structure
type JsonRpcResponse struct {
	JsonRpc string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Result  string        `json:"result,omitempty"`
	Error   *JsonRpcError `json:"error,omitempty"`
}

// JsonRpcError represents a JSON-RPC error
type JsonRpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// WiFiScanResponse represents the top-level response from a WiFi scan
type WiFiScanResponse struct {
	Results []WiFiScanResult `json:"results"`
}

// WiFiScanResult represents a single WiFi network scan result
type WiFiScanResult struct {
	SSID         string         `json:"ssid,omitempty"`
	BSSID        string         `json:"bssid"`
	Mode         string         `json:"mode"`
	Band         int            `json:"band"`
	Channel      int            `json:"channel"`
	MHz          int            `json:"mhz"`
	Signal       int            `json:"signal"`
	Quality      int            `json:"quality"`
	QualityMax   int            `json:"quality_max"`
	HTOperation  *HTOperation   `json:"ht_operation,omitempty"`
	VHTOperation *VHTOperation  `json:"vht_operation,omitempty"`
	Encryption   EncryptionInfo `json:"encryption"`
}

// HTOperation represents 802.11n HT (High Throughput) operation parameters
type HTOperation struct {
	PrimaryChannel         int    `json:"primary_channel"`
	SecondaryChannelOffset string `json:"secondary_channel_offset"`
	ChannelWidth           int    `json:"channel_width"`
}

// VHTOperation represents 802.11ac VHT (Very High Throughput) operation parameters
type VHTOperation struct {
	ChannelWidth int `json:"channel_width"`
	CenterFreq1  int `json:"center_freq_1"`
	CenterFreq2  int `json:"center_freq_2"`
}

// EncryptionInfo represents the encryption/security settings of a WiFi network
type EncryptionInfo struct {
	Enabled        bool     `json:"enabled"`
	WPA            []int    `json:"wpa,omitempty"`
	Authentication []string `json:"authentication,omitempty"`
	Ciphers        []string `json:"ciphers,omitempty"`
}
