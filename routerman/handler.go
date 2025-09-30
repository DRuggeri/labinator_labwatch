package routerman

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ServeHTTP implements the http.Handler interface for RouterManager
func (rm *RouterManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/router/")

	switch {
	case path == "networks" && r.Method == http.MethodGet:
		networks, err := rm.GetWifiNetworks(r.Context())
		if err != nil {
			rm.log.Error("Failed to get WiFi networks", "error", err)
			rm.writeError(w, http.StatusInternalServerError, "Failed to scan WiFi networks", err)
			return
		}

		response := map[string]interface{}{
			"success":  true,
			"networks": networks,
			"count":    len(networks),
		}

		rm.writeJSON(w, http.StatusOK, response)
	case path == "connect" && r.Method == http.MethodPost:
		var req ConnectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			rm.writeError(w, http.StatusBadRequest, "Invalid JSON request body", err)
			return
		}

		if req.SSID == "" {
			rm.writeError(w, http.StatusBadRequest, "SSID is required", nil)
			return
		}

		result, err := rm.ConnectToWifiNetwork(r.Context(), req.SSID, req.Password)
		if err != nil {
			rm.log.Error("Failed to connect to WiFi network", "ssid", req.SSID, "error", err)
			rm.writeError(w, http.StatusInternalServerError, "Failed to connect to WiFi network", err)
			return
		}

		rm.writeJSON(w, http.StatusOK, result)
	case path == "status" && r.Method == http.MethodGet:
		status := map[string]interface{}{
			"success":     true,
			"base_url":    rm.baseURL,
			"username":    rm.username,
			"connected":   rm.sessionID != "",
			"session_id":  rm.sessionID,
			"cache_count": len(rm.networkCache),
		}

		rm.writeJSON(w, http.StatusOK, status)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// ConnectRequest represents the request body for connecting to a WiFi network
type ConnectRequest struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
}

// writeJSON writes a JSON response
func (rm *RouterManager) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		rm.log.Error("Failed to encode JSON response", "error", err)
	}
}

// writeError writes an error response
func (rm *RouterManager) writeError(w http.ResponseWriter, status int, message string, err error) {
	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"success": false,
		"message": message,
	}

	if err != nil {
		response["error"] = err.Error()
	}

	rm.writeJSON(w, status, response)
}
