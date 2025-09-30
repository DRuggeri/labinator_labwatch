// Package common provides shared types and structures for all log watchers.
package common

import (
	"maps"

	"github.com/DRuggeri/labwatch/powerman"
	"github.com/DRuggeri/labwatch/switchman"
	"github.com/DRuggeri/labwatch/talosinitializer"
	"github.com/DRuggeri/labwatch/watchers/callbacks"
	"github.com/DRuggeri/labwatch/watchers/kubernetes"
	"github.com/DRuggeri/labwatch/watchers/openwrt"
	"github.com/DRuggeri/labwatch/watchers/port"
	"github.com/DRuggeri/labwatch/watchers/talos"
)

// LabStatus represents the overall status of the lab infrastructure.
type LabStatus struct {
	Initializer talosinitializer.InitializerStatus `json:"initializer"`
	Kubernetes  kubernetes.KubeStatus              `json:"kubernetes"`
	Talos       map[string]talos.NodeStatus        `json:"talos"`
	Power       powerman.PowerStatus               `json:"power"`
	Switch      switchman.SwitchStatus             `json:"switch"`
	OpenWrt     openwrt.OpenWrtStatus              `json:"openwrt"`
	Ports       port.PortStatus                    `json:"ports"`
	Callbacks   callbacks.CallbackStatus           `json:"callbacks"`
	Logs        LogStats                           `json:"logs"`
}

// Clone creates a deep copy of LabStatus for safe concurrent access
func (ls *LabStatus) Clone() LabStatus {
	clone := *ls

	clone.Kubernetes = kubernetes.KubeStatus{
		Pods:  maps.Clone(ls.Kubernetes.Pods),
		Nodes: maps.Clone(ls.Kubernetes.Nodes),
	}

	if ls.Talos != nil {
		clone.Talos = maps.Clone(ls.Talos)
	}

	if ls.Ports != nil {
		clone.Ports = maps.Clone(ls.Ports)
	}

	if ls.Callbacks.KVPairs != nil || ls.Callbacks.ClientCount != nil {
		clone.Callbacks = callbacks.CallbackStatus{
			KVPairs:     make(map[string]map[string]string),
			ClientCount: make(map[string]int),
		}
		for k := range ls.Callbacks.KVPairs {
			clone.Callbacks.KVPairs[k] = maps.Clone(ls.Callbacks.KVPairs[k])
		}
		clone.Callbacks.ClientCount = maps.Clone(ls.Callbacks.ClientCount)
	}

	clone.Logs = ls.Logs.Clone()

	return clone
}

// LogEvent represents a single log event from any watcher type.
type LogEvent struct {
	Node        string            // The node/host that generated the log
	Service     string            // The service that generated the log
	Level       string            // The log level (info, warn, error, etc.)
	Message     string            // The main log message content
	Attributes  map[string]string // Additional attributes and metadata
	Interesting bool
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

	DHCPServed   map[string]int // Map of DHCP offers made (keyed by IP address)
	ChainServed  map[string]int // Map of hits for the IPXE chain boot file
	IPXEServed   map[string]int // Map of hits for per-machine IPXE file
	AssetsServed map[string]int // Map of hits for assets
}

func NewLogStats() *LogStats {
	return &LogStats{
		DHCPServed:   make(map[string]int),
		ChainServed:  make(map[string]int),
		IPXEServed:   make(map[string]int),
		AssetsServed: make(map[string]int),
	}
}

func (stats *LogStats) Clone() LogStats {
	clone := *stats // Copy all scalar fields

	// Deep copy the maps
	if stats.DHCPServed != nil {
		clone.DHCPServed = maps.Clone(stats.DHCPServed)
	}
	if stats.ChainServed != nil {
		clone.ChainServed = maps.Clone(stats.ChainServed)
	}
	if stats.IPXEServed != nil {
		clone.IPXEServed = maps.Clone(stats.IPXEServed)
	}
	if stats.AssetsServed != nil {
		clone.AssetsServed = maps.Clone(stats.AssetsServed)
	}

	return clone
}

func (stats *LogStats) Add(s LogStats) {
	// Increment all scalar fields
	stats.NumMessages += s.NumMessages

	stats.NumEmergencyMessages += s.NumEmergencyMessages
	stats.NumAlertMessages += s.NumAlertMessages
	stats.NumCriticalMessages += s.NumCriticalMessages
	stats.NumErrorMessages += s.NumErrorMessages
	stats.NumWarnMessages += s.NumWarnMessages
	stats.NumNoticeMessages += s.NumNoticeMessages
	stats.NumInfoMessages += s.NumInfoMessages
	stats.NumDebugMessages += s.NumDebugMessages

	stats.NumDHCPDiscover += s.NumDHCPDiscover
	stats.NumDHCPLeased += s.NumDHCPLeased

	stats.NumDNSQueries += s.NumDNSQueries
	stats.NumDNSLocal += s.NumDNSLocal
	stats.NumDNSRecursions += s.NumDNSRecursions
	stats.NumDNSCached += s.NumDNSCached

	stats.NumCertChecks += s.NumCertChecks
	stats.NumCertOK += s.NumCertOK
	stats.NumCertSigned += s.NumCertSigned
	stats.NumCertRenewed += s.NumCertRenewed

	stats.NumFirewallWanInDrops += s.NumFirewallWanInDrops
	stats.NumFirewallWanOutDrops += s.NumFirewallWanOutDrops
	stats.NumFirewallWanDrops += s.NumFirewallWanDrops
	stats.NumFirewallLanInDrops += s.NumFirewallLanInDrops
	stats.NumFirewallLanOutDrops += s.NumFirewallLanOutDrops
	stats.NumFirewallLanDrops += s.NumFirewallLanDrops

	stats.NumPhysicalPXEBoots += s.NumPhysicalPXEBoots
	stats.NumVirtualPXEBoots += s.NumVirtualPXEBoots

	for key, count := range s.DHCPServed {
		stats.DHCPServed[key] += count
	}
	for key, count := range s.ChainServed {
		stats.ChainServed[key] += count
	}
	for key, count := range s.IPXEServed {
		stats.IPXEServed[key] += count
	}
	for key, count := range s.AssetsServed {
		stats.AssetsServed[key] += count
	}
}
