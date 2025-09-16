package otelfile

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DRuggeri/labwatch/watchers/common"
)

/*
OpenTelemetry log format (JSON lines):
{
  "resourceLogs": [
    {
      "resource": {
        "attributes": [
          {
            "key": "host.name",
            "value": {
              "stringValue": "wally"
            }
          },
          {
            "key": "service.name",
            "value": {
              "stringValue": "kernel"
            }
          }
        ]
      },
      "scopeLogs": [
        {
          "scope": {},
          "logRecords": [
            {
              "timeUnixNano": "1756401581000000000",
              "observedTimeUnixNano": "1756401581995956947",
              "severityNumber": 13,
              "severityText": "warning",
              "body": {
                "kvlistValue": {
                  "values": [
                    {
                      "key": "SYSLOG_IDENTIFIER",
                      "value": {
                        "stringValue": "kernel"
                      }
                    },
                    {
                      "key": "PRIORITY",
                      "value": {
                        "intValue": "4"
                      }
                    },
                    {
                      "key": "message",
                      "value": {
                        "stringValue": "[874004.146653] drop wan in: IN=eth0 OUT= MAC=01:00:5e:00:00:fb:e8:ff:1e:d3:ad:7e:08:00 SRC=192.168.0.15 DST=224.0.0.251 LEN=406 TOS=0x00 PREC=0x00 TTL=255 ID=41075 DF PROTO=UDP SPT=5353 DPT=5353 LEN=386"
                      }
                    }
                  ]
                }
              },
              "traceId": "",
              "spanId": ""
            }
          ]
        }
      ]
    }
  ]
}
*/

// OTLP (OpenTelemetry Protocol) JSON format structures
type otlpLogsData struct {
	ResourceLogs []resourceLogs `json:"resourceLogs"`
}

type resourceLogs struct {
	Resource  resource    `json:"resource"`
	ScopeLogs []scopeLogs `json:"scopeLogs"`
}

type resource struct {
	Attributes []keyValue `json:"attributes"`
}

type scopeLogs struct {
	Scope      scope       `json:"scope"`
	LogRecords []logRecord `json:"logRecords"`
}

type scope struct {
	Name       string     `json:"name,omitempty"`
	Version    string     `json:"version,omitempty"`
	Attributes []keyValue `json:"attributes,omitempty"`
}

type logRecord struct {
	TimeUnixNano         string     `json:"timeUnixNano"`
	ObservedTimeUnixNano string     `json:"observedTimeUnixNano"`
	SeverityNumber       int        `json:"severityNumber"`
	SeverityText         string     `json:"severityText"`
	Body                 anyValue   `json:"body"`
	TraceID              string     `json:"traceId"`
	SpanID               string     `json:"spanId"`
	Attributes           []keyValue `json:"attributes,omitempty"`
}

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type anyValue struct {
	StringValue string      `json:"stringValue,omitempty"`
	IntValue    string      `json:"intValue,omitempty"`
	DoubleValue float64     `json:"doubleValue,omitempty"`
	BoolValue   bool        `json:"boolValue,omitempty"`
	ArrayValue  arrayValue  `json:"arrayValue,omitempty"`
	KvlistValue kvlistValue `json:"kvlistValue,omitempty"`
}

type arrayValue struct {
	Values []anyValue `json:"values"`
}

type kvlistValue struct {
	Values []keyValue `json:"values"`
}

func (w *OtelFileWatcher) normalizeEvents(m []byte) ([]common.LogEvent, common.LogStats) {
	ret := []common.LogEvent{}
	stats := common.LogStats{
		DHCPServed:   make(map[string]int),
		ChainServed:  make(map[string]int),
		IPXEServed:   make(map[string]int),
		AssetsServed: make(map[string]int),
	}

	// Parse OTLP format JSON line
	logsData := otlpLogsData{}
	err := json.Unmarshal(m, &logsData)
	if err != nil {
		// Debug because it happens often with partial lines
		w.log.Debug("error unmarshalling OTLP log record", "error", err, "received", string(m))
		return ret, stats
	}

	// Process each resource log
	for _, resourceLog := range logsData.ResourceLogs {
		// Extract resource attributes (host.name, service.name, etc.)
		resourceAttrs := make(map[string]string)
		for _, attr := range resourceLog.Resource.Attributes {
			resourceAttrs[attr.Key] = extractStringValue(attr.Value)
		}

		// Process each scope's log records
		for _, scopeLog := range resourceLog.ScopeLogs {
			for _, logRec := range scopeLog.LogRecords {
				// Extract the main message from the body
				message := extractMessage(logRec.Body)

				// Extract node and service from resource attributes
				node := resourceAttrs["host.name"]
				service := resourceAttrs["service.name"]

				// Build complete attributes map
				allAttrs := make(map[string]string)

				// Add resource attributes with resource. prefix
				for k, v := range resourceAttrs {
					allAttrs["resource."+k] = v
				}

				// Add log record attributes
				for _, attr := range logRec.Attributes {
					allAttrs[attr.Key] = extractStringValue(attr.Value)
				}

				// Add OTLP-specific fields
				if logRec.TimeUnixNano != "" {
					allAttrs["timeUnixNano"] = logRec.TimeUnixNano
				}
				if logRec.ObservedTimeUnixNano != "" {
					allAttrs["observedTimeUnixNano"] = logRec.ObservedTimeUnixNano
				}
				if logRec.TraceID != "" {
					allAttrs["traceId"] = logRec.TraceID
				}
				if logRec.SpanID != "" {
					allAttrs["spanId"] = logRec.SpanID
				}

				e := common.LogEvent{
					Node:       node,
					Service:    service,
					Message:    message,
					Level:      strings.ToLower(logRec.SeverityText),
					Attributes: allAttrs,
				}

				if w.trace {
					v, _ := json.Marshal(e)
					w.log.Debug("trace", "payload", string(v))
				}

				/*
				   Update stats based on event content
				*/
				stats.NumMessages++

				switch e.Level {
				case "emergency", "fatal":
					stats.NumEmergencyMessages++
				case "alert":
					stats.NumAlertMessages++
				case "critical", "crit":
					stats.NumCriticalMessages++
				case "error", "err":
					stats.NumErrorMessages++
				case "warning", "warn":
					stats.NumWarnMessages++
				case "notice":
					stats.NumNoticeMessages++
				case "info":
					stats.NumInfoMessages++
				case "debug":
					stats.NumDebugMessages++
				default:
					stats.NumInfoMessages++
				}

				if e.Service == "dnsmasq.service" || e.Service == "dnsmasq" {
					if strings.HasPrefix(e.Message, "dnsmasq-dhcp:") {
						if strings.Contains(e.Message, "DHCPDISCOVER") {
							stats.NumDHCPDiscover++
							e.Interesting = true
						} else if strings.Contains(e.Message, "DHCPOFFER") {
							parts := strings.Split(e.Message, " ")
							addr := parts[3]
							stats.NumDHCPLeased++
							stats.DHCPServed[addr]++
							e.Interesting = true
						}
					} else if strings.HasPrefix(e.Message, "query") {
						stats.NumDNSQueries++
						e.Interesting = true
					} else if strings.HasPrefix(e.Message, "dnsmasq: config") {
						stats.NumDNSLocal++
						e.Interesting = true
					} else if strings.HasPrefix(e.Message, "forwarded") {
						stats.NumDNSRecursions++
						e.Interesting = true
					} else if strings.HasPrefix(e.Message, "cached") {
						stats.NumDNSCached++
						e.Interesting = true
					}

				} else if e.Node == "wally" && e.Service == "kernel" {
					if strings.Contains(e.Message, "drop wan in") {
						stats.NumFirewallWanInDrops++
						stats.NumFirewallWanDrops++
						e.Interesting = true
					} else if strings.Contains(e.Message, "drop wan out") {
						stats.NumFirewallWanOutDrops++
						stats.NumFirewallWanDrops++
						e.Interesting = true
					} else if strings.Contains(e.Message, "drop lan in") {
						stats.NumFirewallLanInDrops++
						stats.NumFirewallLanDrops++
						e.Interesting = true
					} else if strings.Contains(e.Message, "drop lan out") {
						stats.NumFirewallLanOutDrops++
						stats.NumFirewallLanDrops++
						e.Interesting = true
					}

				} else if strings.HasPrefix(e.Message, "Starting cert-renewer") {
					stats.NumCertChecks++
					e.Interesting = true
				} else if e.Message == "certificate does not need renewal" {
					stats.NumCertOK++
					e.Interesting = true
				} else if e.Service == "step-ca.service" && strings.Contains(e.Message, "path=/sign") && strings.Contains(e.Message, "status=201") {
					stats.NumCertSigned++
					e.Interesting = true
				} else if e.Service == "step-ca.service" && strings.Contains(e.Message, "path=/renew") && strings.Contains(e.Message, "status=201") {
					stats.NumCertRenewed++
					e.Interesting = true
				}

				if e.Service == "apache2" {
					if uri, ok := e.Attributes["uri"]; ok {
						if uri == "/chain-boot.ipxe" {
							stats.ChainServed[e.Attributes["remote"]]++
							e.Interesting = true
						} else if strings.Contains(uri, "/nodes-ipxe/lab/16") {
							stats.NumPhysicalPXEBoots++
							stats.IPXEServed[e.Attributes["remote"]]++
							e.Interesting = true
						} else if strings.Contains(uri, "/nodes-ipxe/lab/de") {
							stats.NumVirtualPXEBoots++
							stats.IPXEServed[e.Attributes["remote"]]++
							e.Interesting = true
						} else if strings.Contains(uri, "/assets/") {
							stats.AssetsServed[e.Attributes["remote"]]++
							e.Interesting = true
						}
					}
				}

				ret = append(ret, e)
			}
		}
	}

	return ret, stats
}

// Helper function to extract string value from anyValue
func extractStringValue(value anyValue) string {
	if value.StringValue != "" {
		return value.StringValue
	}
	if value.IntValue != "" {
		return value.IntValue
	}
	if value.DoubleValue != 0 {
		return fmt.Sprintf("%.6f", value.DoubleValue)
	}
	if value.BoolValue {
		return "true"
	}
	return ""
}

// Helper function to extract message from log body
func extractMessage(body anyValue) string {
	// Handle simple string body
	if body.StringValue != "" {
		return body.StringValue
	}

	// Handle kvlist body (common in OTLP logs)
	if body.KvlistValue.Values != nil {
		for _, kv := range body.KvlistValue.Values {
			if kv.Key == "message" {
				return extractStringValue(kv.Value)
			}
		}
		// If no "message" key found, try to build a message from all values
		var parts []string
		for _, kv := range body.KvlistValue.Values {
			val := extractStringValue(kv.Value)
			if val != "" {
				parts = append(parts, fmt.Sprintf("%s=%s", kv.Key, val))
			}
		}
		return strings.Join(parts, " ")
	}

	// Handle array body
	if body.ArrayValue.Values != nil {
		var parts []string
		for _, val := range body.ArrayValue.Values {
			str := extractStringValue(val)
			if str != "" {
				parts = append(parts, str)
			}
		}
		return strings.Join(parts, " ")
	}

	return ""
}
