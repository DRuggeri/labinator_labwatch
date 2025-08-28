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

func (w *OtelFileWatcher) normalizeEvents(m []byte) []common.LogEvent {
	ret := []common.LogEvent{}

	// Parse OTLP format JSON line
	logsData := otlpLogsData{}
	err := json.Unmarshal(m, &logsData)
	if err != nil {
		w.log.Error("error unmarshalling OTLP log record", "error", err, "received", string(m))
		return ret
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

				ret = append(ret, e)
			}
		}
	}

	return ret
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
