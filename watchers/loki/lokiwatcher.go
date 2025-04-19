package loki

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var reconnectDuration = time.Duration(250) * time.Millisecond
var sleepDuration = time.Duration(250) * time.Millisecond
var QUERY = `{ host_name =~ ".+" } | json`

type LogEvent struct {
	Node       string
	Service    string
	Level      string
	Message    string
	Attributes map[string]string
}

type LogStats struct {
	NumMessages int

	NumEmergencyMessages int
	NumAlertMessages     int
	NumCriticalMessages  int
	NumErrorMessages     int
	NumWarnMessages      int
	NumNoticeMessages    int
	NumInfoMessages      int
	NumDebugMessages     int

	NumDHCPDiscover int
	NumDHCPLeased   int

	NumDNSQueries    int
	NumDNSLocal      int
	NumDNSRecursions int
	NumDNSCached     int

	NumCertChecks  int
	NumCertOK      int
	NumCertSigned  int
	NumCertRenewed int

	NumFirewallWanInDrops  int
	NumFirewallWanOutDrops int
	NumFirewallLanInDrops  int
	NumFirewallLanOutDrops int

	IPXETalosChainload int
}

type LokiWatcherConfig struct {
	ReconnectDuration time.Duration `yaml:"reconnect-duration"`
	Address           string        `yaml:"address"`
}
type LokiWatcher struct {
	url              url.URL
	trace            bool
	lastTs           int
	internalLogChan  chan LogEvent
	internalStatChan chan LogStats
	stats            LogStats
	log              *slog.Logger
}

func NewLokiWatcher(ctx context.Context, addr string, query string, trace bool, log *slog.Logger) (*LokiWatcher, error) {
	if log == nil {
		log = slog.Default()
	}
	if query == "" {
		query = QUERY
	}

	q := url.Values{}
	q.Set("limit", "9999")
	q.Set("query", query)

	return &LokiWatcher{
		url: url.URL{
			Scheme:   "wss",
			Host:     addr,
			Path:     "/loki/api/v1/tail",
			RawQuery: q.Encode(),
		},
		trace:            trace,
		internalLogChan:  make(chan LogEvent),
		internalStatChan: make(chan LogStats),
		lastTs:           int(time.Now().UnixMicro()) * 1000,
		log:              log.With("operation", "LokiWatcher"),
	}, nil
}

func (w *LokiWatcher) Watch(controlContext context.Context, eventChan chan<- LogEvent, statChan chan<- LogStats) {
	go func() {
		for {
			select {
			case <-controlContext.Done():
				return
			default:
				// Not ready to read from control channel - carry on
			}

			c, _, err := websocket.DefaultDialer.Dial(w.url.String(), nil)
			if err != nil {
				w.log.Error("error connecting to Loki", "error", err)
				time.Sleep(reconnectDuration)
				continue
			}

			w.log.Info("connected to Loki")
			for {
				select {
				case <-controlContext.Done():
					return
				default:
					// Not ready to read from control channel - carry on
				}

				w.log.Debug("attempting to read...")
				_, message, err := c.ReadMessage()
				if err != nil {
					log.Println("read:", err)
					break
				}

				w.log.Debug(fmt.Sprintf("read %d bytes", len(message)))
				events := w.normalizeEvents(message)
				w.log.Debug(fmt.Sprintf("Got %d events back after normalization", len(events)))

				if len(events) > 0 {
					for _, e := range events {
						w.internalLogChan <- e
					}
					w.updateStats(events)
					w.internalStatChan <- w.stats
				}
			}
			if c != nil {
				c.Close()
			}
		}
	}()

	for {
		select {
		case <-controlContext.Done():
			return
		default:
			// Not ready to read from control channel - carry on
		}

		// See if there are any messsages to read from the clients
	OUTER:
		for {
			select {
			case event := <-w.internalLogChan:
				eventChan <- event
			case stats := <-w.internalStatChan:
				statChan <- stats
			default:
				break OUTER
			}
		}

		time.Sleep(sleepDuration)
	}
}

/*
	{
	  "streams": [
		{
			"stream": {
				"bytes": "432",
				"code": "404",
				"detected_level": "unknown",
				"host_name": "boss",
				"httpver": "HTTP/1.1",
				"log_file_name": "other_vhosts_access.log",
				"logname": "-",
				"method": "GET",
				"observed_timestamp": "1745074909238069850",
				"port": "80",
				"referrer": "-",
				"remote": "127.0.0.1",
				"service_name": "apache2",
				"uri": "/foobar",
				"user": "-",
				"useragent": "curl/7.88.1",
				"vhost": "boss.local"
			},
			"values": [
				[
				"1745074909000000000",
				"boss.local:80 127.0.0.1 - - [19/Apr/2025:10:01:49 -0500] \"GET /foobar HTTP/1.1\" 404 432 \"-\" \"curl/7.88.1\""
				]
			]
		},
	    {
	      "stream": {
	        "MESSAGE": "dnsmasq: query[A] google.com from 192.168.122.3",
	        "PRIORITY": "6",
	        "SYSLOG_IDENTIFIER": "dnsmasq",
	        "detected_level": "info",
	        "host_name": "boss",
	        "level": "info",
	        "observed_timestamp": "1743347949795772204",
	        "service_name": "dnsmasq.service",
	        "severity_number": "9",
	        "severity_text": "info"
	      },
	      "values": [
	        [
	          "1743347949347380000",
	          "{\"MESSAGE\":\"dnsmasq: query[A] google.com from 192.168.122.3\",\"PRIORITY\":\"6\",\"SYSLOG_IDENTIFIER\":\"dnsmasq\"}"
	        ]
	      ]
	    }
	  ]
	}
*/
type lokiMsg struct {
	Streams []lokiStream
}
type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]string        `json:"values"`
}

func (w *LokiWatcher) normalizeEvents(m []byte) []LogEvent {
	ret := []LogEvent{}
	msg := lokiMsg{}
	err := json.Unmarshal(m, &msg)
	if err != nil {
		w.log.Error("error unmarshalling", "error", err, "received", string(m))
		return ret
	}

	if w.trace {
		w.log.Debug("trace", "payload", string(m))
	}

	for _, stream := range msg.Streams {
		thisTs, _ := strconv.Atoi(stream.Values[0][0])
		if thisTs > w.lastTs {
			w.lastTs = thisTs
		} else {
			continue
		}

		// This message is newer than the last batch of messages
		e := LogEvent{
			Node:       stream.Stream["host_name"],
			Service:    stream.Stream["service_name"],
			Message:    stream.Stream["MESSAGE"],
			Level:      stream.Stream["level"],
			Attributes: stream.Stream,
		}
		ret = append(ret, e)
	}
	return ret
}

func (w *LokiWatcher) updateStats(events []LogEvent) {
	for _, e := range events {
		w.stats.NumMessages++

		switch e.Level {
		case "emergency":
			w.stats.NumEmergencyMessages++
		case "alert":
			w.stats.NumAlertMessages++
		case "critical":
			w.stats.NumCriticalMessages++
		case "error":
			w.stats.NumErrorMessages++
		case "warning":
			w.stats.NumWarnMessages++
		case "notice":
			w.stats.NumNoticeMessages++
		case "info":
			w.stats.NumInfoMessages++
		case "debug":
			w.stats.NumDebugMessages++
		default:
			w.stats.NumInfoMessages++
		}

		if e.Service == "dnsmasq.service" {
			if strings.HasPrefix(e.Message, "dnsmasq-dhcp:") {
				if strings.Contains(e.Message, "DHCPDISCOVER") {
					w.stats.NumDHCPDiscover++
				} else if strings.Contains(e.Message, "DHCPACK") {
					w.stats.NumDHCPLeased++
				}
			} else if strings.HasPrefix(e.Message, "query") {
				w.stats.NumDNSQueries++
			} else if strings.HasPrefix(e.Message, "dnsmasq: config") {
				w.stats.NumDNSLocal++
			} else if strings.HasPrefix(e.Message, "forwarded") {
				w.stats.NumDNSRecursions++
			} else if strings.HasPrefix(e.Message, "cached") {
				w.stats.NumDNSCached++
			}

		} else if e.Node == "wally" && e.Service == "kernel" {
			if strings.Contains(e.Message, "drop wan in") {
				w.stats.NumFirewallWanInDrops++
			} else if strings.Contains(e.Message, "drop wan out") {
				w.stats.NumFirewallWanOutDrops++
			} else if strings.Contains(e.Message, "drop lan in") {
				w.stats.NumFirewallLanInDrops++
			} else if strings.Contains(e.Message, "drop lan out") {
				w.stats.NumFirewallLanOutDrops++
			}

		} else if strings.HasPrefix(e.Message, "Starting cert-renewer") {
			w.stats.NumCertChecks++
		} else if e.Message == "certificate does not need renewal" {
			w.stats.NumCertOK++
		} else if e.Service == "step-ca.service" && strings.Contains(e.Message, "path=/sign") && strings.Contains(e.Message, "status=201") {
			w.stats.NumCertSigned++
		} else if e.Service == "step-ca.service" && strings.Contains(e.Message, "path=/renew") && strings.Contains(e.Message, "status=201") {
			w.stats.NumCertRenewed++
		}

		if e.Service == "apache2.service" {
			if strings.Contains(e.Message, "GET /talos-boot.ipxe") {
				w.stats.IPXETalosChainload++
			}
		}
	}
}
