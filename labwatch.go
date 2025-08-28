package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/DRuggeri/labwatch/handlers/browserhandler"
	"github.com/DRuggeri/labwatch/handlers/eventhandler"
	"github.com/DRuggeri/labwatch/handlers/loadstathandler"
	"github.com/DRuggeri/labwatch/handlers/statushandler"
	"github.com/DRuggeri/labwatch/lablinkmanager"
	"github.com/DRuggeri/labwatch/powerman"
	"github.com/DRuggeri/labwatch/statusinator"
	"github.com/DRuggeri/labwatch/talosinitializer"
	"github.com/DRuggeri/labwatch/watchers/callbacks"
	"github.com/DRuggeri/labwatch/watchers/common"
	"github.com/DRuggeri/labwatch/watchers/kubernetes"
	"github.com/DRuggeri/labwatch/watchers/otelfile"
	"github.com/DRuggeri/labwatch/watchers/port"
	"github.com/DRuggeri/labwatch/watchers/talos"
	"github.com/DRuggeri/labwatch/wm"
	"github.com/alecthomas/kingpin/v2"
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
	LogPath             string `yaml:"log-path"`
	LogTrace            bool   `yaml:"log-trace"`
	TalosConfigFile     string `yaml:"talos-config"`
	TalosScenarioConfig string `yaml:"talos-scenario-config"`
	TalosScenariosDir   string `yaml:"talos-scenarios-directory"`
	PowerManagerPort    string `yaml:"powermanager-port"`
	StatusinatorPort    string `yaml:"statusinator-port"`
	NetbootFolder       string `yaml:"netboot-folder"`
	NetbootLink         string `yaml:"netboot-link"`
	PortWatchTrace      bool   `yaml:"port-watch-trace"`
	PodWatchNamespace   string `yaml:"pod-watch-namespace"`
}

func main() {
	kingpin.Version(Version)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()

	activeLab := ""
	activeLabMutex := &sync.Mutex{}

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
		LogPath:             "/var/tmplog/otelcol.jsonl",
		LogTrace:            false,
		TalosConfigFile:     "/home/boss/.talos/config",
		PowerManagerPort:    "/dev/serial/by-id/usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0",
		StatusinatorPort:    "/dev/serial/by-id/usb-Espressif_USB_JTAG_serial_debug_unit_98:3D:AE:E9:29:08-if00",
		NetbootFolder:       "/var/www/html/nodes-ipxe/",
		NetbootLink:         "lab",
		TalosScenarioConfig: "/home/boss/talos/scenarios/configs.yaml",
		TalosScenariosDir:   "/home/boss/talos/scenarios",
		PortWatchTrace:      false,
		PodWatchNamespace:   "",
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

	mainCtx, propagateShutdown := context.WithCancel(context.Background())

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

	// Create status and event handlers
	statusWatcher, statusHandler, err := statushandler.NewStatusWatcher(mainCtx, log)
	if err != nil {
		log.Error("failed to create status handlers", "error", err.Error())
		os.Exit(1)
	}

	eventReceiveHandler, eventSendHandler, err := eventhandler.NewEventWatcher(mainCtx, log)
	if err != nil {
		log.Error("failed to create event handlers", "error", err.Error())
		os.Exit(1)
	}

	// Set up statusinator with the handlers
	statusinatorStatusChan := make(chan common.LabStatus, 2)
	statusWatcher.AddClient("statusinator", statusinatorStatusChan)
	statusinatorEventChan := make(chan common.LogEvent, 5)
	eventReceiveHandler.AddClient("statusinator", statusinatorEventChan)
	go statinator.Watch(mainCtx, statusinatorStatusChan, statusinatorEventChan)

	wMan := wm.NewWindowsManager(mainCtx, log)
	if err != nil {
		log.Error("failed to create the windows manager", "error", err.Error())
		os.Exit(1)
	}

	cbWatcher, _ := callbacks.NewCallbackWatcher(mainCtx, log)

	watcherCancel, err := startWatchers(mainCtx, cfg, activeLab, pMan, cbWatcher, nil, statusWatcher, eventReceiveHandler, log)
	if err != nil {
		log.Error("failed to start watchers", "error", err.Error())
		os.Exit(1)
	}

	http.Handle("/power", pMan)
	http.Handle("/callbacks", cbWatcher)

	resetWatchers := func() {
		log.Info("resetting watchers")
		watcherCancel()
		cbWatcher.Reset()
		watcherCancel, err = startWatchers(mainCtx, cfg, activeLab, pMan, cbWatcher, initializer, statusWatcher, eventReceiveHandler, log)
		if err != nil {
			log.Error("failed to restart watchers", "error", err.Error())
			os.Exit(1)
		}
	}

	http.HandleFunc("/resetwatchers", func(w http.ResponseWriter, r *http.Request) {
		resetWatchers()
	})

	http.HandleFunc("/getlab", func(w http.ResponseWriter, r *http.Request) {
		activeLabMutex.Lock()
		lab := activeLab
		activeLabMutex.Unlock()
		w.Write([]byte(lab))
	})

	http.HandleFunc("/lastlab", func(w http.ResponseWriter, r *http.Request) {
		lastLab := initializer.GetLastLab()
		if lastLab == "" {
			log.Error("failed to get last lab")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(lastLab))
	})

	http.HandleFunc("/setlab", func(w http.ResponseWriter, r *http.Request) {
		adopt := false
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
		if adopt = r.URL.Query().Get("adopt") == "true"; adopt {
			lab = initializer.GetLastLab()
			log.Info("adopting running lab", "lab", lab)
		}

		if lab == "" {
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

		activeLabMutex.Lock()
		activeLab = lab
		activeLabMutex.Unlock()

		// Check if we are just "adopting" an already running lab
		if adopt {
			resetWatchers()
			w.WriteHeader(http.StatusOK)
			return
		}

		// Already running a lab? Bail on it
		if initializerCancel != nil {
			initializerCancel()
		}

		log.Info("configuring lab", "lab", activeLab, "numWatchEndpoints", len(endpoints))
		labMan := lablinkmanager.NewLinkManager(cfg.NetbootFolder, cfg.NetbootLink, activeLab)
		labMan.EnableLab()

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

		initializerCtx, initializerCancel = context.WithCancel(mainCtx)
		go initializer.Initialize(initializerCtx, lab, labMan, pMan)
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
		log.Debug("system command run", "output", string(out), "error", err)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	http.Handle("/status", statusHandler)
	http.Handle("/events", eventSendHandler)

	browserHandler, _ := browserhandler.NewBrowserHandler(log)
	http.HandleFunc("/navigate", func(w http.ResponseWriter, r *http.Request) {
		wMan.Kill(wm.WMScreenBottom)
		browserHandler.ServeHTTP(w, r)
	})

	http.HandleFunc("/launch", func(w http.ResponseWriter, r *http.Request) {
		command := strings.ToLower(r.URL.Query().Get("command"))
		var cmd *exec.Cmd

		switch command {
		case "htop":
			cmd = exec.Command("lxterminal", "--command", `htop`)
		case "top":
			cmd = exec.Command("lxterminal", "--command", `top -d 1`)
		case "journal":
			cmd = exec.Command("lxterminal", "--command", `journalctl -q -f`)
		case "apache":
			cmd = exec.Command("lxterminal", "--command", `tail -F /var/log/apache2/other_vhosts_access.log`)
		case "tcpdump":
			cmd = exec.Command("lxterminal", "--command", `sudo tcpdump -n`)
		case "loki":
			cmd = exec.Command("lxterminal", "--command", `bash -c 'LOKI_ADDR=https://boss.local:3100 logcli-linux-amd64 query --output=jsonl -f "{ host_name =~ \".+\" } | json "'`)
		case "vnc":
			node, _ := strconv.Atoi(r.URL.Query().Get("node"))
			vm, _ := strconv.Atoi(r.URL.Query().Get("vm"))
			if node < 1 || node > 6 || vm < 1 || vm > 9 {
				log.Info("bunk node and vm provided", "node", node, "vm", vm)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			cmd = exec.Command("xtightvncviewer", fmt.Sprintf("node%d.local::590%d", node, vm))
		default:
			log.Info("request command not defined", "command", command)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		wMan.StartWindow(mainCtx, cmd, wm.WMScreenBottom)
	})

	loadStatReceiveHandler, loadStatSendHandler, err := loadstathandler.NewStatHandlers(mainCtx, log)
	http.Handle("/loadstats", loadStatReceiveHandler)
	http.Handle("/loadinfo", loadStatSendHandler)

	http.HandleFunc("/scenarios", func(w http.ResponseWriter, r *http.Request) {
		input, err := os.ReadFile(cfg.TalosScenarioConfig)
		if err != nil {
			log.Error("failed to read scenario config file", "error", err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var data interface{}
		yaml.Unmarshal(input, &data)

		var scenarioName string
		if r.URL.Query().Get("current") == "true" {
			activeLabMutex.Lock()
			scenarioName = activeLab
			activeLabMutex.Unlock()
		}

		if r.URL.Query().Get("last") == "true" {
			scenarioName = initializer.GetLastLab()
		}

		if tmp := r.URL.Query().Get("name"); tmp != "" {
			scenarioName = tmp
		}

		if scenarioName != "" {
			tmp, ok := data.(map[string]interface{})
			if !ok {
				log.Info("could not parse scenario config file")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if tmp2, ok := tmp[scenarioName]; ok {
				data = tmp2
			} else {
				log.Info("requested scenario not found", "name", scenarioName)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		b, _ := json.Marshal(data)
		w.WriteHeader(http.StatusOK)
		w.Write(b)
	})

	fsys, err := fs.Sub(siteFS, "site")
	if err != nil {
		log.Error("failed to set up site", "error", err.Error())
		os.Exit(1)
	}

	fileServer := http.FileServer(http.FS(fsys))
	http.Handle("/", fileServer)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	go func() {
		sig := <-sigChan
		sigName := "ARG"
		switch sig {
		case syscall.SIGTERM:
			sigName = "SIGTERM"
		case syscall.SIGHUP:
			sigName = "SIGHUP"
		case syscall.SIGINT:
			sigName = "SIGINT"
		}
		log.Info("received shutdown signal", "signal", sigName)
		propagateShutdown()
		abortTime := time.Now().Add(time.Second * 5)
		for statinator.Running() && time.Now().Before(abortTime) {
			time.Sleep(time.Millisecond * 100)
		}
		os.Exit(0)
	}()

	err = http.ListenAndServe(":8080", nil)
	log.With("operation", "main", "error", err.Error()).Info("shutting down")
}

func startWatchers(ctx context.Context, cfg LabwatchConfig, lab string, pMan *powerman.PowerManager, cbWatcher *callbacks.CallbackWatcher, initializer *talosinitializer.TalosInitializer, statusReceiveHandler *statushandler.StatusWatcher, eventReceiveHandler *eventhandler.EventReceiveHandler, log *slog.Logger) (context.CancelFunc, error) {
	log = log.With("operation", "startWatchers")

	var err error
	var portBroadcast chan<- port.PortStatus
	var cbBroadcast chan<- callbacks.CallbackStatus
	var iInfo <-chan talosinitializer.InitializerStatus
	var cbStatus <-chan callbacks.CallbackStatus
	endpoints := []string{}

	if cbWatcher != nil {
		cbStatus = cbWatcher.GetStatusUpdateChan()
		if initializer != nil {
			cbBroadcast = initializer.GetCBChan()
		}
	}
	if initializer != nil {
		portBroadcast = initializer.GetPortChan()
		iInfo = initializer.GetStatusUpdateChan()
		endpoints, err = initializer.GetWatchEndpoints(lab)
		if err != nil {
			return nil, err
		}
	}

	log.Debug("starting watchers", "endpoints", endpoints, "portchan", portBroadcast != nil)
	ctx, cancel := context.WithCancel(ctx)

	tInfo := make(chan map[string]talos.NodeStatus)
	/*
		if lab != "" {
			tWatcher, err := talos.NewTalosWatcher(ctx, cfg.TalosConfigFile, lab, log)
			if err != nil {
				cancel()
				return nil, err
			}
			go tWatcher.Watch(ctx, tInfo)
		}
	*/

	// lWatcher, err := loki.NewLokiWatcher(ctx, cfg.LokiAddress, cfg.LokiQuery, cfg.LokiTrace, log)
	lWatcher, err := otelfile.NewOtelFileWatcher(ctx, cfg.LogPath, cfg.LogTrace, log)
	if err != nil {
		cancel()
		return nil, err
	}
	events := make(chan common.LogEvent)
	stats := make(chan common.LogStats)
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

	kubeWatcher, err := kubernetes.NewKubeWatcher("", cfg.PodWatchNamespace, log)
	if err != nil {
		cancel()
		return nil, err
	}
	kubeInfo := make(chan map[string]kubernetes.PodStatus)
	if lab != "" {
		go kubeWatcher.Watch(ctx, kubeInfo)
	}

	log = log.With("operation", "watchloop")
	go func() {
		for {
			broadcastStatusUpdate := false
			var updatedStatus common.LabStatus
			select {
			case <-ctx.Done():
				return
			case i, ok := <-iInfo:
				if ok {
					updatedStatus = statusReceiveHandler.GetCurrentStatus()
					updatedStatus.Initializer = i
					broadcastStatusUpdate = true
				} else {
					log.Error("error encountered reading initializer status")
				}
			case t, ok := <-tInfo:
				if ok {
					updatedStatus = statusReceiveHandler.GetCurrentStatus()
					updatedStatus.Talos = t
					broadcastStatusUpdate = true
				} else {
					log.Error("error encountered reading talos states")
				}
			case p, ok := <-pInfo:
				if ok {
					updatedStatus = statusReceiveHandler.GetCurrentStatus()
					updatedStatus.Power = p
					broadcastStatusUpdate = true
				} else {
					log.Error("error encountered reading power status")
				}
			case k, ok := <-kubeInfo:
				if ok {
					updatedStatus = statusReceiveHandler.GetCurrentStatus()
					updatedStatus.Kubernetes = k
					broadcastStatusUpdate = true
				} else {
					log.Error("error encountered reading kubernetes pod status")
				}
			case c, ok := <-cbStatus:
				if ok {
					updatedStatus = statusReceiveHandler.GetCurrentStatus()
					updatedStatus.Callbacks = c
					broadcastStatusUpdate = true
					if cbBroadcast != nil {
						if len(cbBroadcast) == cap(cbBroadcast) {
							log.Error("not broadcasting callback update - channel is full", "len", len(cbBroadcast), "cap", cap(cbBroadcast))
						} else {
							log.Debug("broadcasting callback info")
							cbBroadcast <- c
							log.Debug("broadcast done")
						}

					}
				} else {
					log.Error("error encountered reading callback states")
				}
			case p, ok := <-portInfo:
				if ok {
					updatedStatus = statusReceiveHandler.GetCurrentStatus()
					updatedStatus.Ports = p
					broadcastStatusUpdate = true
					if portBroadcast != nil {
						if len(portBroadcast) == cap(portBroadcast) {
							log.Error("not broadcasting port update - channel is full", "len", len(portBroadcast), "cap", cap(portBroadcast))
						} else {
							log.Debug("broadcasting port info")
							portBroadcast <- p
							log.Debug("broadcast done")
						}
					}
				} else {
					log.Error("error encountered reading port states")
				}
			case s, ok := <-stats:
				if ok {
					updatedStatus = statusReceiveHandler.GetCurrentStatus()
					updatedStatus.Logs = s
					broadcastStatusUpdate = true
				} else {
					log.Error("error encountered reading log stats")
				}
			case e, ok := <-events:
				if ok {
					eventReceiveHandler.BroadcastEvent(e)
				} else {
					log.Error("error encountered reading events")
				}
			default:
				time.Sleep(time.Millisecond * 100)
				continue
			}

			if broadcastStatusUpdate {
				statusReceiveHandler.UpdateStatus(updatedStatus)
			}
		}
	}()
	log.Info("watchers started", "iInfo", iInfo)

	return cancel, nil
}
