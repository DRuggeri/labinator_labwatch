package talos

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/siderolabs/gen/xslices"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	tclient "github.com/siderolabs/talos/pkg/machinery/client"
	tcconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
)

// SEE: https://github.com/siderolabs/talos/blob/main/pkg/machinery/client/client.go
// SEE: https://github.com/siderolabs/talos/blob/main/cmd/talosctl/cmd/talos/events.go
var reconnectDuration = time.Duration(250) * time.Millisecond
var sleepDuration = time.Duration(250) * time.Millisecond

type TalosWatcher struct {
	config       *tcconfig.Config
	client       *tclient.Client
	Status       map[string]NodeStatus
	talosContext *tcconfig.Context
	watchers     map[string]NodeWatcher
	internalChan chan NodeStatus
	log          *slog.Logger
}

type NodeWatcher struct {
	CurrentStatus NodeStatus
	configOpts    []tclient.OptionFunc
	log           *slog.Logger
}

type NodeStatus struct {
	WatcherState    ConnectionState
	Node            string
	Phase           map[string]string
	Tasks           map[string]string
	Services        map[string]ServiceStatus
	Sequences       map[string]string
	Error           error
	Addresses       []string
	Stage           string
	Ready           bool
	UnmetConditions []string
}

type ServiceStatus struct {
	State      string
	Message    string
	Healthy    HealthState
	LastChange time.Time
}

type HealthState string

const HEALTH_UNKNOWN HealthState = "unknown"
const HEALTH_OK HealthState = "healthy"
const HEALTH_ERR HealthState = "unhealthy"

type ConnectionState string

const CONNECTION_OK ConnectionState = "connected"
const CONNECTION_DISCONNECTED ConnectionState = "disconnected"

// func NewTalosWatcher(configFile string, clusterName string) (watchers.Watcher, error) {
func NewTalosWatcher(ctx context.Context, configFile string, clusterName string, log *slog.Logger) (*TalosWatcher, error) {
	w := &TalosWatcher{
		Status:       map[string]NodeStatus{},
		watchers:     map[string]NodeWatcher{},
		internalChan: make(chan NodeStatus),
		log:          log.With("operation", "TalosWatcher"),
	}

	cfg, err := tcconfig.Open(configFile)
	if err != nil {
		return nil, err
	}
	w.config = cfg

	var tctx *tcconfig.Context
	var ok bool
	if tctx, ok = cfg.Contexts[clusterName]; !ok {
		return nil, fmt.Errorf("the %s context name does not exist in the config file %s", clusterName, configFile)
	}
	w.talosContext = tctx

	if len(tctx.Nodes) == 0 {
		return nil, fmt.Errorf("there are no nodes defined in the %s config file", configFile)
	}

	client, err := tclient.New(context.Background(), tclient.WithConfig(cfg))
	if err != nil {
		return nil, err
	}
	w.client = client

	//Create a standalone client that can suffer connects/disconnects without affecting the overall client
	for _, nodeName := range tctx.Nodes {
		backoffConfig := backoff.DefaultConfig
		backoffConfig.MaxDelay = time.Duration(1) * time.Second

		nodeWatcher := NodeWatcher{
			CurrentStatus: NodeStatus{
				WatcherState:    CONNECTION_DISCONNECTED,
				Node:            nodeName,
				Phase:           map[string]string{},
				Tasks:           map[string]string{},
				Services:        map[string]ServiceStatus{},
				Sequences:       map[string]string{},
				Addresses:       []string{},
				Stage:           "unknown",
				UnmetConditions: []string{},
			},
			configOpts: []tclient.OptionFunc{
				tclient.WithConfig(cfg),
				tclient.WithContextName(clusterName),
				tclient.WithEndpoints(nodeName),
				tclient.WithGRPCDialOptions(grpc.WithConnectParams(
					grpc.ConnectParams{
						Backoff: backoffConfig,
					},
				)),
			},
			log: log.With("operation", "NodeWatcher", "node", nodeName),
		}
		go nodeWatcher.Watch(ctx, w.internalChan)
		w.watchers[nodeName] = nodeWatcher
	}

	return w, err
}

func (w *TalosWatcher) Watch(controlContext context.Context, resultChan chan<- map[string]NodeStatus) {
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
			case nodeStatus := <-w.internalChan:
				w.Status[nodeStatus.Node] = nodeStatus

				// Make a copy of all data to send to the chan
				og, _ := json.Marshal(w.Status)
				cpy := map[string]NodeStatus{}
				json.Unmarshal(og, &cpy)

				resultChan <- cpy
			default:
				break OUTER
			}
		}

		time.Sleep(sleepDuration)
	}
}

func (w NodeWatcher) Watch(controlContext context.Context, resultChan chan<- NodeStatus) {
	log := w.log.With("operation", "TalosWatcher.Watch")
	log.Debug("watching")
	resultChan <- w.CurrentStatus

	// Modelled from https://github.com/siderolabs/talos/blob/main/cmd/talosctl/cmd/talos/events.go
	fxn := func(c <-chan tclient.Event) {
		event := <-c

		switch msg := event.Payload.(type) {
		case *machine.SequenceEvent:
			if msg.Error != nil {
				w.CurrentStatus.Sequences[msg.Sequence] = msg.GetError().GetMessage()
			} else {
				w.CurrentStatus.Sequences[msg.Sequence] = msg.GetAction().String()
			}
		case *machine.PhaseEvent:
			w.CurrentStatus.Phase[msg.GetPhase()] = msg.GetAction().String()
		case *machine.TaskEvent:
			w.CurrentStatus.Tasks[msg.GetTask()] = msg.GetAction().String()
		case *machine.ServiceStateEvent:
			health, lastChange := getHealthInfo(msg.GetHealth())
			w.CurrentStatus.Services[msg.GetService()] = ServiceStatus{
				State:      msg.GetAction().String(),
				Message:    msg.GetMessage(),
				Healthy:    health,
				LastChange: lastChange,
			}
		case *machine.ConfigLoadErrorEvent:
			w.CurrentStatus.Error = fmt.Errorf("config load: %s", msg.GetError())
		case *machine.ConfigValidationErrorEvent:
			w.CurrentStatus.Error = fmt.Errorf("config validation: %s", msg.GetError())
		case *machine.AddressEvent:
			w.CurrentStatus.Addresses = msg.GetAddresses()
		case *machine.MachineStatusEvent:
			w.CurrentStatus.Stage = msg.GetStage().String()
			w.CurrentStatus.Ready = msg.GetStatus().Ready
			unmet := xslices.Map(msg.GetStatus().GetUnmetConditions(),
				func(c *machine.MachineStatusEvent_MachineStatus_UnmetCondition) string {
					return c.Name
				},
			)
			w.CurrentStatus.UnmetConditions = unmet
		}

		// Send status after every event
		resultChan <- w.CurrentStatus
	}

	for {
		select {
		case <-controlContext.Done():
			return
		default:
			// Not ready to read from control channel - carry on
		}

		connectTimeout := time.Duration(1) * time.Second
		watchContext, killWatch := context.WithCancel(controlContext)
		connectCtx, closeCtx := context.WithTimeout(watchContext, connectTimeout)

		log.Debug("creating new client")
		nodeClient, err := tclient.New(connectCtx, w.configOpts...)
		if err != nil {
			fmt.Printf("client error: %s\n", err.Error())
		} else {
			go func() {
				conn := nodeClient.Conn()
				newState := CONNECTION_DISCONNECTED
				conn.WaitForStateChange(controlContext, conn.GetState())
				for {
					bail := false
					connState := conn.GetState()
					switch connState {
					case connectivity.Connecting:
					case connectivity.Ready:
						w.CurrentStatus.WatcherState = CONNECTION_OK
					case connectivity.Idle:
						bail = true
					case connectivity.TransientFailure:
						bail = true
					case connectivity.Shutdown:
						bail = true
					default:
						bail = true
					}

					if w.CurrentStatus.WatcherState != newState {
						w.log.Debug("detected state change", "old", w.CurrentStatus.WatcherState, "new", newState)
						w.CurrentStatus.WatcherState = newState
						resultChan <- w.CurrentStatus
					}

					if bail {
						log.Debug("killing watch")
						killWatch()
						conn.Close()
						return
					}
					conn.WaitForStateChange(watchContext, connState)
				}
			}()

			// Populate some services right off the bat
			/*
				var remotePeer peer.Peer
				services := []string{"apid", "kubelet", "containerd"}
				for _, serviceName := range services {
					svc, err := nodeClient.ServiceInfo(watchContext, serviceName, grpc.Peer(&remotePeer))
					if err != nil {
						if svc == nil {
							fmt.Printf("error listing service: %s", err.Error())
							continue
						}
					}

					health, lastChange := getHealthInfo(svc[0].Service.Health)
					w.CurrentStatus.Services[serviceName] = ServiceStatus{
						State:      svc[0].Service.State,
						Message:    "",
						Healthy:    health,
						LastChange: lastChange,
					}
				}
			*/

			nodeClient.EventsWatch(watchContext, fxn)
		}
		closeCtx()
		killWatch()

		time.Sleep(reconnectDuration)
	}
}

func getHealthInfo(h *machine.ServiceHealth) (HealthState, time.Time) {
	health := HEALTH_UNKNOWN
	if !h.Unknown {
		if h.Healthy {
			health = HEALTH_OK
		} else {
			health = HEALTH_ERR
		}
	}
	return health, h.LastChange.AsTime()
}
