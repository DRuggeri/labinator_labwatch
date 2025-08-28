// Package common provides shared types and structures for all log watchers.
package common

import (
	"github.com/DRuggeri/labwatch/powerman"
	"github.com/DRuggeri/labwatch/talosinitializer"
	"github.com/DRuggeri/labwatch/watchers/callbacks"
	"github.com/DRuggeri/labwatch/watchers/kubernetes"
	"github.com/DRuggeri/labwatch/watchers/port"
	"github.com/DRuggeri/labwatch/watchers/talos"
)

// LabStatus represents the overall status of the lab infrastructure.
type LabStatus struct {
	Initializer talosinitializer.InitializerStatus `json:"initializer"`
	Kubernetes  map[string]kubernetes.PodStatus    `json:"kubernetes"`
	Talos       map[string]talos.NodeStatus        `json:"talos"`
	Power       powerman.PowerStatus               `json:"power"`
	Ports       port.PortStatus                    `json:"ports"`
	Callbacks   callbacks.CallbackStatus           `json:"callbacks"`
	Logs        LogStats                           `json:"logs"`
}

// LogEvent represents a single log event from any watcher type.
type LogEvent struct {
	Node       string            // The node/host that generated the log
	Service    string            // The service that generated the log
	Level      string            // The log level (info, warn, error, etc.)
	Message    string            // The main log message content
	Attributes map[string]string // Additional attributes and metadata
}

// LogStats contains statistical information about processed log events.
type LogStats struct {
	NumMessages int // Total number of messages processed

	// Message counts by severity level
	NumEmergencyMessages int
	NumAlertMessages     int
	NumCriticalMessages  int
	NumErrorMessages     int
	NumWarnMessages      int
	NumNoticeMessages    int
	NumInfoMessages      int
	NumDebugMessages     int

	// DHCP-related statistics
	NumDHCPDiscover int
	NumDHCPLeased   int

	// DNS-related statistics
	NumDNSQueries    int
	NumDNSLocal      int
	NumDNSRecursions int
	NumDNSCached     int

	// Certificate-related statistics
	NumCertChecks  int
	NumCertOK      int
	NumCertSigned  int
	NumCertRenewed int

	// Firewall-related statistics
	NumFirewallWanInDrops  int
	NumFirewallWanOutDrops int
	NumFirewallWanDrops    int
	NumFirewallLanInDrops  int
	NumFirewallLanOutDrops int
	NumFirewallLanDrops    int

	// PXE boot statistics
	NumPhysicalPXEBoots int
	NumVirtualPXEBoots  int
}
