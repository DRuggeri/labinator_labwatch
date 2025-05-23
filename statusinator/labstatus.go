package statusinator

import (
	"github.com/DRuggeri/labwatch/powerman"
	"github.com/DRuggeri/labwatch/talosinitializer"
	"github.com/DRuggeri/labwatch/watchers/loki"
	"github.com/DRuggeri/labwatch/watchers/port"
	"github.com/DRuggeri/labwatch/watchers/talos"
)

type LabStatus struct {
	Initializer talosinitializer.InitializerStatus `json:"initializer"`
	Talos       map[string]talos.NodeStatus        `json:"talos"`
	Power       powerman.PowerStatus               `json:"power"`
	Ports       port.PortStatus                    `json:"ports"`
	Logs        loki.LogStats                      `json:"logs"`
}
