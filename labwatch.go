package main

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/DRuggeri/labwatch/browserhandler"
	"github.com/DRuggeri/labwatch/powerman"
	"github.com/DRuggeri/labwatch/statusinator"
	"github.com/DRuggeri/labwatch/talosinitializer"
	"github.com/DRuggeri/labwatch/watchers/loki"
	"github.com/DRuggeri/labwatch/watchers/port"
	"github.com/DRuggeri/labwatch/watchers/talos"
	"github.com/alecthomas/kingpin/v2"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"

	_ "net/http/pprof"
)

//go:embed all:site
var siteFS embed.FS

var (
	Version  = "testing"
	logLevel = kingpin.Flag("log-level", "Log Level (one of debug|info|warn|error)").Short('l').Envar("LABWATCH_LOGLEVEL").String()
	config   = kingpin.Flag("config", "Configuration file path").Short('c').Envar("LABWATCH_CONFIG").ExistingFile()
)

type LabwatchConfig struct {
	LokiAddress         string `yaml:"loki-address"`
	LokiQuery           string `yaml:"loki-query"`
	LokiTrace           bool   `yaml:"loki-trace"`
	TalosConfigFile     string `yaml:"talos-config"`
	TalosScenarioConfig string `yaml:"talos-scenario-config"`
	TalosScenariosDir   string `yaml:"talos-scenarios-directory"`
	PowerManagerPort    string `yaml:"powermanager-port"`
	StatusinatorPort    string `yaml:"statusinator-port"`
	NetbootFolder       string `yaml:"netboot-folder"`
	NetbootLink         string `yaml:"netboot-link"`
	PortWatchTrace      bool   `yaml:"port-watch-trace"`
}

var currentStatus = statusinator.LabStatus{}
var statusClients = map[string]chan<- statusinator.LabStatus{}
var eventClients = map[string]chan<- loki.LogEvent{}
var lock = &sync.Mutex{}

func main() {
	kingpin.Version(Version)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()

	activeLab := ""

	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	switch *logLevel {
	case "error":
		opts.Level = slog.LevelError
	case "warn":
		opts.Level = slog.LevelWarn
	case "info":
		opts.Level = slog.LevelInfo
	case "debug":
		opts.Level = slog.LevelDebug
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, opts)).With("operation", "main")
	log.Info("starting up labwatch", "version", Version)

	cfg := LabwatchConfig{
		LokiAddress:         "boss.local:3100",
		LokiQuery:           `{ host_name =~ ".+" } | json`,
		LokiTrace:           false,
		TalosConfigFile:     "/home/boss/.talos/config",
		PowerManagerPort:    "/dev/serial/by-id/usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0",
		StatusinatorPort:    "/dev/serial/by-id/usb-Espressif_USB_JTAG_serial_debug_unit_98:3D:AE:E9:29:08-if00",
		NetbootFolder:       "/var/www/html/nodes-ipxe/",
		NetbootLink:         "lab",
		TalosScenarioConfig: "/home/boss/talos/scenarios/configs.yaml",
		TalosScenariosDir:   "/home/boss/talos/scenarios",
		PortWatchTrace:      false,
	}

	if *config != "" {
		d, err := os.ReadFile(*config)
		if err != nil {
			log.Error("failed to read provided config file", "error", err.Error())
			os.Exit(1)
		}
		err = yaml.Unmarshal(d, &cfg)
		if err != nil {
			log.Error("failed to parse provided config file", "error", err.Error())
			os.Exit(1)
		}
	}

	var initializerCtx context.Context
	var initializerCancel context.CancelFunc
	initializer, err := talosinitializer.NewTalosInitializer(cfg.TalosConfigFile, cfg.TalosScenarioConfig, cfg.TalosScenariosDir, "koob", log)
	if err != nil {
		log.Error("failed to create the Talos initializer", "error", err.Error())
		os.Exit(1)
	}

	pMan, err := powerman.NewPowerManager(cfg.PowerManagerPort, log)
	if err != nil {
		log.Error("failed to create the power manager", "error", err.Error())
		os.Exit(1)
	}

	statinator, err := statusinator.NewStatusinator(cfg.StatusinatorPort, log)
	if err != nil {
		log.Error("failed to create the power manager", "error", err.Error())
		os.Exit(1)
	}
	statusinatorStatusChan := make(chan statusinator.LabStatus, 2)
	addStatusClient("statusinator", statusinatorStatusChan)
	statusinatorEventChan := make(chan loki.LogEvent, 5)
	addEventClient("statusinator", statusinatorEventChan)
	go statinator.Watch(context.Background(), statusinatorStatusChan, statusinatorEventChan)

	watcherCancel, err := startWatchers(cfg, activeLab, pMan, nil, log)
	if err != nil {
		log.Error("failed to start watchers", "error", err.Error())
		os.Exit(1)
	}

	log.Info("watchers initialized")

	u := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}

	http.Handle("/power", pMan)

	resetWatchers := func() {
		log.Info("resetting watchers")
		watcherCancel()
		watcherCancel, err = startWatchers(cfg, activeLab, pMan, initializer, log)
		if err != nil {
			log.Error("failed to restart watchers", "error", err.Error())
			os.Exit(1)
		}
	}

	http.HandleFunc("/resetwatchers", func(w http.ResponseWriter, r *http.Request) {
		resetWatchers()
	})

	http.HandleFunc("/getlab", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(activeLab))
	})

	http.HandleFunc("/setlab", func(w http.ResponseWriter, r *http.Request) {
		// Already running a lab? Bail on it
		if initializerCancel != nil {
			initializerCancel()
		}

		defer func() {
			if err := recover(); err != nil { //catch
				log.Error("failed setting lab", "error", err, "stack", debug.Stack())
			}
		}()

		lab := strings.ToLower(r.URL.Query().Get("lab"))
		if _, err := os.Stat(filepath.Join(cfg.NetbootFolder, lab)); err != nil {
			log.Info("requested lab not defined", "lab", lab)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		endpoints, err := initializer.GetWatchEndpoints(lab)
		if err != nil {
			log.Info("could not get endpoints", "lab", lab, "error", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(endpoints) == 0 {
			log.Error("could not find endpoints for lab", "lab", lab)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		activeLab = lab

		log.Info("configuring lab", "lab", activeLab, "numWatchEndpoints", len(endpoints))
		os.Remove(filepath.Join(cfg.NetbootFolder, cfg.NetbootLink))
		os.Symlink(filepath.Join(cfg.NetbootFolder, activeLab), filepath.Join(cfg.NetbootFolder, cfg.NetbootLink))

		// Ensure boxes are down
		pMan.TurnOff(powerman.PALL)
		// Wait for all to be off
		for {
			if pMan.PortIsOff(powerman.PALL) {
				break
			}
			time.Sleep(time.Millisecond * 100)
		}

		resetWatchers()

		initializerCtx, initializerCancel = context.WithCancel(context.Background())
		go initializer.Initialize(initializerCtx, lab, pMan)
	})

	http.HandleFunc("/system", func(w http.ResponseWriter, r *http.Request) {
		action := strings.ToLower(r.URL.Query().Get("action"))
		var args []string
		switch action {
		case "shutdown":
			args = []string{"/usr/bin/sudo", "/usr/sbin/poweroff"}
		case "restart":
			args = []string{"/usr/bin/sudo", "/usr/sbin/reboot"}
		default:
			log.Info("invalid system action requested", "action", action)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		log.Info("performing system action", "action", action)
		// Kill the boxen
		pMan.TurnOff(powerman.P1)
		pMan.TurnOff(powerman.P2)
		pMan.TurnOff(powerman.P3)
		pMan.TurnOff(powerman.P4)
		pMan.TurnOff(powerman.P5)
		pMan.TurnOff(powerman.P6)

		// Do the needy to this box
		out, err := exec.Command(args[0], args[1:]...).Output()
		log.Debug("system command run", "output", string(out), "error", err.Error())
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			b, _ := json.Marshal(currentStatus)
			w.Write(b)
			return
		}

		conn, err := u.Upgrade(w, r, nil)
		if err != nil {
			slog.Info("upgrade failed", "error", err.Error())
			return
		}

		thisChan := make(chan statusinator.LabStatus)
		uuid := uuid.New().String()

		addStatusClient(uuid, thisChan)
		defer removeStatusClient(uuid)

		data, _ := json.Marshal(currentStatus)
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			slog.Info("write failed", "error", err.Error())
			return
		}

		for {
			var status statusinator.LabStatus
			select {
			case <-r.Context().Done():
				return
			case status = <-thisChan:
			}
			data, _ := json.Marshal(status)
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	})

	http.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		conn, err := u.Upgrade(w, r, nil)
		if err != nil {
			slog.Info("upgrade failed", "error", err.Error())
			return
		}

		thisChan := make(chan loki.LogEvent)
		uuid := uuid.New().String()

		addEventClient(uuid, thisChan)
		defer removeEventClient(uuid)

		for {
			select {
			case <-r.Context().Done():
				return
			case e := <-thisChan:
				data, _ := json.Marshal(e)
				if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
					return
				}
			}
		}
	})

	browserHandler, _ := browserhandler.NewBrowserHandler(log)
	http.Handle("/navigate", browserHandler)

	fsys, err := fs.Sub(siteFS, "site")
	if err != nil {
		log.Error("failed to set up site", "error", err.Error())
		os.Exit(1)
	}

	fileServer := http.FileServer(http.FS(fsys))
	http.Handle("/", fileServer)

	err = http.ListenAndServe(":8080", nil)
	log.With("operation", "main", "error", err.Error()).Info("shutting down")
}

func startWatchers(cfg LabwatchConfig, lab string, pMan *powerman.PowerManager, initializer *talosinitializer.TalosInitializer, log *slog.Logger) (context.CancelFunc, error) {
	log = log.With("operation", "startWatchers")

	var err error
	var portBroadcast chan<- port.PortStatus
	var iInfo <-chan talosinitializer.InitializerStatus
	endpoints := []string{}

	if initializer != nil {
		portBroadcast = initializer.GetPortChan()
		iInfo = initializer.GetStatusUpdateChan()
		endpoints, err = initializer.GetWatchEndpoints(lab)
		if err != nil {
			return nil, err
		}
	}

	log.Debug("starting watchers", "endpoints", endpoints, "portchan", portBroadcast != nil)
	status := statusinator.LabStatus{}
	ctx, cancel := context.WithCancel(context.Background())

	tInfo := make(chan map[string]talos.NodeStatus)
	if lab != "" {
		tWatcher, err := talos.NewTalosWatcher(ctx, cfg.TalosConfigFile, lab, log)
		if err != nil {
			cancel()
			return nil, err
		}
		go tWatcher.Watch(ctx, tInfo)
	}

	lWatcher, err := loki.NewLokiWatcher(ctx, cfg.LokiAddress, cfg.LokiQuery, cfg.LokiTrace, log)
	if err != nil {
		cancel()
		return nil, err
	}
	events := make(chan loki.LogEvent)
	stats := make(chan loki.LogStats)
	go lWatcher.Watch(ctx, events, stats)

	pInfo := make(chan powerman.PowerStatus)
	go pMan.Watch(ctx, pInfo)

	portWatcher, err := port.NewPortWatcher(ctx, endpoints, cfg.PortWatchTrace, log)
	if err != nil {
		cancel()
		return nil, err
	}
	portInfo := make(chan port.PortStatus)
	go portWatcher.Watch(ctx, portInfo)

	log = log.With("operation", "watchloop")
	go func() {
		for {
			broadcastStatusUpdate := false
			select {
			case <-ctx.Done():
				return
			case i, ok := <-iInfo:
				if ok {
					status.Initializer = i
					broadcastStatusUpdate = true
				} else {
					log.Error("error encountered reading initializer status")
				}
			case t, ok := <-tInfo:
				if ok {
					status.Talos = t
					broadcastStatusUpdate = true
				} else {
					log.Error("error encountered reading talos states")
				}
			case p, ok := <-pInfo:
				if ok {
					status.Power = p
					broadcastStatusUpdate = true
				} else {
					log.Error("error encountered reading power status")
				}
			case p, ok := <-portInfo:
				if ok {
					status.Ports = p
					broadcastStatusUpdate = true
					if portBroadcast != nil {
						portBroadcast <- p
					}
				} else {
					log.Error("error encountered reading port states")
				}
			case s, ok := <-stats:
				if ok {
					status.Logs = s
					broadcastStatusUpdate = true
				} else {
					log.Error("error encountered reading log stats")
				}
			case e, ok := <-events:
				if ok {
					if len(eventClients) > 0 {
						log.Debug("broadcasting event", "clients", len(eventClients))
						for _, ch := range eventClients {
							ch <- e
						}
					}
				} else {
					log.Error("error encountered reading events")
				}
			default:
				time.Sleep(time.Millisecond * 100)
				continue
			}

			if broadcastStatusUpdate {
				currentStatus = status
				if len(statusClients) > 0 {
					log.Debug("broadcasting status", "clients", len(statusClients))
					for _, ch := range statusClients {
						ch <- status
					}
				}
			}
		}
	}()

	return cancel, nil
}

func addStatusClient(id string, ch chan<- statusinator.LabStatus) {
	lock.Lock()
	statusClients[id] = ch
	lock.Unlock()
}

func removeStatusClient(id string) {
	lock.Lock()
	delete(statusClients, id)
	lock.Unlock()
}

func addEventClient(id string, ch chan<- loki.LogEvent) {
	lock.Lock()
	eventClients[id] = ch
	lock.Unlock()
}

func removeEventClient(id string) {
	lock.Lock()
	delete(eventClients, id)
	lock.Unlock()
}
