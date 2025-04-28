package port

import (
	"context"
	"log/slog"
	"net"
	"time"
)

var sleepDuration = time.Duration(100) * time.Millisecond
var connectionCheckInterval = time.Second
var recheckInterval = time.Duration(5) * time.Second

type singlePortStatus struct {
	endpoint string
	status   bool
}

type PortStatus map[string]bool

type PortWatcher struct {
	endpoints          map[string]bool
	internalStatusChan chan singlePortStatus
	log                *slog.Logger
}

func NewPortWatcher(ctx context.Context, endpoints []string, log *slog.Logger) (*PortWatcher, error) {
	if log == nil {
		log = slog.Default()
	}

	e := map[string]bool{}
	for _, tmp := range endpoints {
		e[tmp] = false
	}

	return &PortWatcher{
		endpoints:          e,
		internalStatusChan: make(chan singlePortStatus),
		log:                log.With("operation", "PortWatcher"),
	}, nil
}

func (w *PortWatcher) Watch(controlContext context.Context, resultChan chan<- PortStatus) {
	resultChan <- w.endpoints

	for endpoint := range w.endpoints {
		go func() {
			// Always start with 'false' to detect if something came up before the watcher started
			up := false

			for {
				select {
				case <-controlContext.Done():
					return
				default:
				}

				current := up
				var sleepyTime time.Duration
				conn, err := net.DialTimeout("tcp", endpoint, time.Second)
				if err != nil {
					up = false
					sleepyTime = connectionCheckInterval
				} else {
					up = true
					conn.Close()
					sleepyTime = recheckInterval
				}

				// Only notify on change in status
				if up != current {
					w.internalStatusChan <- singlePortStatus{endpoint: endpoint, status: up}
				}

				time.Sleep(sleepyTime)
			}
		}()
	}

	for {
		select {
		case <-controlContext.Done():
			return
		case s := <-w.internalStatusChan:
			w.endpoints[s.endpoint] = s.status
			resultChan <- w.endpoints
		default:
			// Not ready to read from control channel or watcher - carry on
		}
		time.Sleep(sleepDuration)
	}
}
