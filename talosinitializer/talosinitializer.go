package talosinitializer

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/DRuggeri/labwatch/lablinkmanager"
	"github.com/DRuggeri/labwatch/powerman"
	"github.com/DRuggeri/labwatch/watchers/callbacks"
	"github.com/DRuggeri/labwatch/watchers/port"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type TalosInitializer struct {
	talosConfig    string
	scenarioConfig map[string]ScenarioConfig
	nodesDir       string
	lastLabFile    string
	lastStepStart  time.Time
	log            *slog.Logger
	k8sContext     string
	portStatuses   chan port.PortStatus
	cbStatuses     chan callbacks.CallbackStatus
	statusChan     chan InitializerStatus
}

type InitializerStatus struct {
	LabName                string
	NumHypervisors         int
	InitializedHypervisors int
	NumNodes               int
	InitializedNodes       int
	NumPods                int
	InitializedPods        int
	CurrentStep            string
	Failed                 bool
	TimeSpent              map[string]int
}

type ScenarioConfig struct {
	Nodes map[string]NodeConfig `yaml:"nodes"`
}

type NodeConfig struct {
	IP          string         `yaml:"ip"`
	MAC         string         `yaml:"mac"`
	Name        string         `yaml:"name"`
	Role        string         `yaml:"role"`
	Type        string         `yaml:"type"`
	InstallDisk string         `yaml:"installdisk"`
	Hypervisor  HypervisorInfo `yaml:"hypervisor"`
	configFile  string
}

type HypervisorInfo struct {
	IP  string `yaml:"ip"`
	MAC string `yaml:"mac"`
}

func NewTalosInitializer(talosConfigFile string, scenarioConfigFile string, scenarioNodesDir string, k8sContext string, log *slog.Logger) (*TalosInitializer, error) {
	d, err := os.ReadFile(scenarioConfigFile)
	if err != nil {
		return nil, err
	}

	cfg := map[string]ScenarioConfig{}
	err = yaml.Unmarshal(d, &cfg)
	if err != nil {
		return nil, err
	}

	// Add in node names from key for ease of reference later
	for scenarioName, scenarioCfg := range cfg {
		for name, nodeCfg := range scenarioCfg.Nodes {
			if nodeCfg.Type == "hypervisor" {
				continue
			}

			nodeCfg.Name = name
			nodeCfg.configFile = filepath.Join(scenarioNodesDir, scenarioName, fmt.Sprintf("node-%s.yaml", name))
		}
	}

	valid := []string{}
	for k := range cfg {
		valid = append(valid, k)
	}
	log.Info("TalosInitializer configured", "scenarios", valid)

	return &TalosInitializer{
		talosConfig:    talosConfigFile,
		scenarioConfig: cfg,
		nodesDir:       scenarioNodesDir,
		lastLabFile:    filepath.Join(scenarioNodesDir, "lastlab"),
		k8sContext:     k8sContext,
		log:            log.With("operation", "TalosInitializer"),
		portStatuses:   make(chan port.PortStatus, 5),
		cbStatuses:     make(chan callbacks.CallbackStatus, 5),
		statusChan:     make(chan InitializerStatus, 5),
	}, nil
}

func (i *TalosInitializer) GetWatchEndpoints(scenario string) ([]string, error) {
	endpoints := []string{}
	config, ok := i.scenarioConfig[scenario]
	if !ok {
		return nil, fmt.Errorf("the scenario %s does not exist", scenario)
	}

	hypervisors, nodeNames := config.GetHypervisorsAndNodes()

	for ip := range hypervisors {
		endpoints = append(endpoints, fmt.Sprintf("%s:22", ip))
	}

	for _, name := range nodeNames {
		endpoints = append(endpoints, fmt.Sprintf("%s:50000", config.Nodes[name].IP))
	}
	return endpoints, nil
}

func (i *TalosInitializer) GetPortChan() chan<- port.PortStatus {
	return i.portStatuses
}

func (i *TalosInitializer) GetCBChan() chan<- callbacks.CallbackStatus {
	return i.cbStatuses
}

func (i *TalosInitializer) GetStatusUpdateChan() <-chan InitializerStatus {
	return i.statusChan
}

func (i *TalosInitializer) GetLastLab() string {
	lastLab, _ := os.ReadFile(i.lastLabFile)
	return string(lastLab)
}

func (i *TalosInitializer) updateStep(s *InitializerStatus, step string, log *slog.Logger) {
	if s.CurrentStep == step {
		return
	}
	now := time.Now()
	dur := now.Sub(i.lastStepStart)
	s.TimeSpent[s.CurrentStep] = int(dur.Seconds())

	i.lastStepStart = time.Now()
	s.CurrentStep = step
	log.Info("initialization state updated", "step", s.CurrentStep, "lastStepDuration", dur)
	i.sendStatusUpdate(*s)
}

func (i *TalosInitializer) sendStatusUpdate(s InitializerStatus) {
	select {
	case i.statusChan <- s:
	default:
		// No listeners - avoid blocking
		i.log.Debug("discarding status update - would block", "len", len(i.statusChan), "cap", cap(i.statusChan))
	}
}

func (i *TalosInitializer) Initialize(controlContext context.Context, scenario string, labMan *lablinkmanager.LinkManager, pMan *powerman.PowerManager) {
	config, ok := i.scenarioConfig[scenario]
	log := i.log.With("scenario", scenario)

	if !ok {
		log.Error("scenario does not exist")
		return
	}

	hypervisors, nodeNames := config.GetHypervisorsAndNodes()

	b, _ := os.ReadFile(i.lastLabFile)
	lastLab := string(b)
	labHasPhysicalNodes := false
	needDiskWipe := false
	diskWipeDone := false

	nodeEndpointMap := map[string]NodeConfig{}
	hypervisorEndpointMap := map[string]string{}
	diskWipeMap := map[string]NodeConfig{}

	i.lastStepStart = time.Now()

	// Drain any port or callback updates on the chans to start fresh
	// Store as a func to do it again later in case of disk wipe
	drainChans := func() {
	DRAIN:
		for {
			select {
			case <-i.portStatuses:
			case <-i.cbStatuses:
			default:
				break DRAIN
			}
		}
	}
	drainChans()

	for ip := range hypervisors {
		hypervisorEndpointMap[fmt.Sprintf("%s:22", ip)] = ip
	}

	for _, name := range nodeNames {
		node := config.Nodes[name]
		nodeEndpointMap[fmt.Sprintf("%s:50000", node.IP)] = node

		// Physical or hypervisor nodes are bare metal so their disk needs to be
		// wiped before launching a lab using a physical Talos node. This is only
		// required for a Talos node (since the hypervisors autowipe), but it doesn't
		// hurt to do all of them if we have to do one of them
		switch node.Type {
		case "physical":
			labHasPhysicalNodes = true
			fallthrough
		case "hypervisor":
			diskWipeMap[node.IP] = node
		}
	}

	if labHasPhysicalNodes && (strings.Contains(lastLab, "hybrid") || strings.Contains(lastLab, "physical")) {
		needDiskWipe = true
	}

	log.Info("initializing scenario", "numHypervisors", len(hypervisors), "numNodes", len(nodeNames), "labHasPhysicalNodes", labHasPhysicalNodes, "needDiskWipe", needDiskWipe)

	// Always consume the callbacks to avoid blocking the main loop
	go func() {
		log := i.log.With("operation", "initializerCallbackLoop")
		for {
			select {
			case <-controlContext.Done():
				return
			case s := <-i.cbStatuses:
				log.Debug("received callback")
				if needDiskWipe {
					tmp := true
					for ip := range diskWipeMap {
						if s.KVPairs[ip] == nil || s.KVPairs[ip]["wipe"] == "" {
							tmp = false
						}
					}

					if tmp && !diskWipeDone {
						// Toggle on diskWipeDone so we can continue spinning
						// here and consuming channel messages
						log.Info("disk wipes complete")
						diskWipeDone = true
						labMan.EnableLab()
					}
				}
			}
		}
	}()

	// We need to wipe disks first - start that now while secret generation runs
	if needDiskWipe {
		labMan.EnableDiskWipe()
		pMan.TurnOn(powerman.PALL)
	}

	initStatus := &InitializerStatus{
		NumHypervisors: len(hypervisors),
		NumNodes:       len(nodeNames),
		CurrentStep:    "init",
		LabName:        scenario,
		TimeSpent:      map[string]int{},
	}
	i.updateStep(initStatus, "secret gen", log)

	// Set up the configs
	command := exec.CommandContext(controlContext, filepath.Join(i.nodesDir, scenario, "generate.sh"))
	result, err := command.CombinedOutput()
	log.Debug("run result", "command", command, "error", err, "output", string(result))
	if err != nil || !strings.Contains(string(result), "Victory is mine!") {
		log.Error("secret generation failed", "output", string(result))
		initStatus.Failed = true
		i.sendStatusUpdate(*initStatus)
		return
	}

	if needDiskWipe && !diskWipeDone {
		i.updateStep(initStatus, "disk wipe", log)
		for !diskWipeDone {
			time.Sleep(time.Millisecond * 100)
		}
		pMan.TurnOff(powerman.PALL)

		for {
			if pMan.PortIsOff(powerman.PALL) {
				break
			}
			time.Sleep(time.Millisecond * 100)
		}
		time.Sleep(time.Second)
		drainChans()
	}

	// Boot the boxes! We also set the lastlab so we can know if the disks
	// potentially need to be wiped
	i.updateStep(initStatus, "powerup", log)
	err = os.WriteFile(i.lastLabFile, []byte(scenario), 0644)
	if err != nil {
		log.Error("failed to write lastlab file", "file", i.lastLabFile, "error", err.Error())
	}
	pMan.TurnOn(powerman.PALL)

	step := "booting-nodes"
	if len(hypervisors) > 0 {
		step = "booting-hypervisors"
	}
	i.updateStep(initStatus, step, log)

	log.Debug("entering initialization loop")
	nextDebugStatus := time.Now().Add(5 * time.Second)
	currentStatus := port.PortStatus{}
INITLOOP:
	for {
		select {
		case <-controlContext.Done():
			return
		case s := <-i.portStatuses:
			log.Debug("port status change detected", "status", s)
			endpointsUp := []string{}
			statusCopy := port.PortStatus{}

			// Get list of ports that came up
			for endpoint, up := range s {
				statusCopy[endpoint] = up
				if !currentStatus[endpoint] && up {
					endpointsUp = append(endpointsUp, endpoint)
					log.Info("endpoint has come up", "endpoint", endpoint)
				}
			}

			// Take the next action on the port
			for _, endpoint := range endpointsUp {
				if ip, ok := hypervisorEndpointMap[endpoint]; ok {
					log.Info(fmt.Sprintf("hypervisor is up - starting %d VMs", len(hypervisors[ip])), "ip", ip)
					hypervisorAddr := hypervisorEndpointMap[endpoint]
					delete(hypervisorEndpointMap, endpoint)
					initStatus.InitializedHypervisors++
					go StartVMsOnHypervisor(controlContext, hypervisorAddr, hypervisors[ip], log)

				} else if node, ok := nodeEndpointMap[endpoint]; ok {
					log.Info("node is up", "name", node.Name)
					delete(nodeEndpointMap, endpoint)
					initStatus.InitializedNodes++
					//ConfigureNode(node.IP, node.configFile, scenario, log)
				} else {
					log.Error("discarding port up information because it is not a known hypervisor or node", "endpoint", endpoint)
				}
			}

			// Copy statuses
			currentStatus = statusCopy

			// All done initializing stuff?
			if len(hypervisorEndpointMap) == 0 {
				step = "booting-nodes"
				if len(nodeEndpointMap) == 0 {
					break INITLOOP
				}
			} else {
				step = "booting-hypervisors"
			}

			i.updateStep(initStatus, step, log)
			i.sendStatusUpdate(*initStatus)
		default:
			// Not cancelled and no port updates - emit a status update for debug?
			time.Sleep(100 * time.Millisecond)
			if nextDebugStatus.After(time.Now()) {
				continue INITLOOP
			}
		}

		todoHypervisors := []string{}
		for n := range hypervisorEndpointMap {
			todoHypervisors = append(todoHypervisors, n)
		}
		slices.Sort(todoHypervisors)

		todoNodes := []string{}
		for n := range nodeEndpointMap {
			todoNodes = append(todoNodes, n)
		}
		slices.Sort(todoNodes)

		log.Debug("initializations to complete",
			"numHypervisors", len(hypervisorEndpointMap),
			"numNodes", len(nodeEndpointMap),
			"hypervisors", strings.Join(todoHypervisors, ","),
			"nodes", strings.Join(todoNodes, ","),
		)
		nextDebugStatus = time.Now().Add(5 * time.Second)
	}
	log.Debug("completed initialization loop")

	// We're done blocking on port updates - start discarding them
	go func() {
		for {
			select {
			case <-controlContext.Done():
				return
			case s := <-i.portStatuses:
				log.Debug("port status change ignored post-init", "status", s)
			}
		}
	}()

	// All the nodes have been reconfigured so we can now bootstrap via any control plane node
	for name, node := range config.Nodes {
		if node.Role == "controlplane" {
			i.updateStep(initStatus, "bootstrapping", log)
			log.Info("bootstrapping Kubernetes through control plane node", "node", name)
			err = BootstrapEtcd(controlContext, i.talosConfig, node.IP, scenario, i.k8sContext, log)
			if err != nil {
				log.Error("bootstrapping etcd failed", "error", err.Error())
				initStatus.Failed = true
				i.sendStatusUpdate(*initStatus)
				return
			}
			break
		}
	}

	// Start watching pods as soon as we can run kubectl
	podWatchCtx, podWatchCancel := context.WithCancel(controlContext)
	go i.watchPods(podWatchCtx, initStatus, log)

	// Install additional stuff
	i.updateStep(initStatus, "finalizing", log)
	err = FinalizeInstall(controlContext, log)
	if err != nil {
		log.Error("finalizing install failed", "output", string(result))
		initStatus.Failed = true
		i.sendStatusUpdate(*initStatus)
		podWatchCancel()
		return
	}

	i.updateStep(initStatus, "starting", log)
	log.Info("awaiting pod starts")
	for initStatus.NumPods != 0 && initStatus.NumPods != initStatus.InitializedPods {
		time.Sleep(100 * time.Millisecond)
	}
	podWatchCancel()

	i.updateStep(initStatus, "done", log)
	log.Info("initialization complete")
}

func (config ScenarioConfig) GetHypervisorsAndNodes() (map[string][]NodeConfig, []string) {
	hypervisors := map[string][]NodeConfig{}
	nodes := []string{}

	for _, cfg := range config.Nodes {
		if cfg.Hypervisor.IP != "" {
			if hypervisors[cfg.Hypervisor.IP] == nil {
				hypervisors[cfg.Hypervisor.IP] = []NodeConfig{}
			}

			hypervisors[cfg.Hypervisor.IP] = append(hypervisors[cfg.Hypervisor.IP], cfg)
		}

		if cfg.Type != "hypervisor" {
			nodes = append(nodes, cfg.Name)
		}
	}

	return hypervisors, nodes
}

func StartVMsOnHypervisor(ctx context.Context, ip string, nodes []NodeConfig, log *slog.Logger) {
	for i, node := range nodes {
		for {
			startCommand := "" +
				fmt.Sprintf("virsh destroy %s", node.Name) +
				fmt.Sprintf(";virsh undefine %s --remove-all-storage", node.Name) +
				fmt.Sprintf(";qemu-img create -f qcow2 /var/lib/libvirt/images/%s.qcow2 10G", node.Name) +
				fmt.Sprintf(";virt-install --name %s", node.Name) +
				fmt.Sprintf("  --disk /var/lib/libvirt/images/%s.qcow2,device=disk,bus=virtio", node.Name) +
				fmt.Sprintf("  --graphics vnc,listen=0.0.0.0,port=%d", 5901+i) +
				fmt.Sprintf("  --network network=default,model=virtio,mac=%s", node.MAC) +
				fmt.Sprintf("  --console file,path=/var/log/vm-console-%s.log", node.Name) +
				"              --memory 2048 --vcpus 2 --os-variant ubuntu22.10 --virt-type kvm" +
				"              --boot network,useserial=on --noautoconsole --import"

				/*
					Error message in /var/log/vm-console-w4-virtual.log:
					iPXE 1.0.0+git-20190125.36a4c85-5.1 -- Open Source Network Boot Firmware -- http
					://ipxe.org
					Features: DNS HTTP iSCSI NFS TFTP AoE ELF MBOOT PXE bzImage Menu PXEXT

					net0: de:ad:be:ef:30:04 using virtio-net on 0000:01:00.0 (open)
					[Link:up, TX:0 TXE:0 RX:0 RXE:0]
					Configuring (net0 de:ad:be:ef:30:04).................. ok
					net0: 192.168.122.34/255.255.255.0 gw 192.168.122.1
					net0: fd00:ab:cd:0:dcad:beff:feef:3004/64
					net0: fe80::dcad:beff:feef:3004/64
					Next server: 192.168.122.3
					Filename: http://boss.local/chain-boot.ipxe
					http://boss.local/chain-boot.ipxe.................. Connection timed out (http:/
					/ipxe.org/4c116035)
					No more network devices

					No bootable device.
				*/

				/*
					Success message in /var/log/vm-console-w4-virtual.log:
					iPXE 1.0.0+git-20190125.36a4c85-5.1 -- Open Source Network Boot Firmware -- http
					://ipxe.org
					Features: DNS HTTP iSCSI NFS TFTP AoE ELF MBOOT PXE bzImage Menu PXEXT

					Press Ctrl-B for the iPXE command line...
					net0: de:ad:be:ef:30:04 using virtio-net on 0000:01:00)
					[Link:up, TX:0 TXE:0 RX:0 RXE:0]
					Configuring (net0 de:ad:be:ef:30:04).................. ok
					net0: 192.168.122.34/255.255.255.0 gw 192.168.122.1
					net0: fe80::dcad:beff:feef:3004/64
					Next server: 192.168.122.3
					Filename: http://boss.local/chain-boot.ipxe
					http://boss.local/chain-boot.ipxe... ok
					chain-boot.ipxe : 47 bytes [script]
					nodes-ipxe/lab/de-ad-be-ef-30-04.ipxe... ok
					/assets/talos-vmlinuz-amd64.xz... ok
					/assets/talos-initramfs-amd64.xz... 64%     ok
				*/

			command := exec.CommandContext(ctx, "ssh",
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				fmt.Sprintf("root@%s", ip),
				startCommand,
			)

			result, err := command.CombinedOutput()

			// Check if context was cancelled before processing result (resulting in an expected error)
			select {
			case <-ctx.Done():
				log.Debug("context cancelled, stopping VM creation", "node", node.Name)
				return
			default:
			}

			if !strings.Contains(string(result), "Domain creation completed") {
				log.Error("failed to start", "startcommand", startCommand, "error", err, "hypervisor", ip, "output", string(result))
				time.Sleep(time.Second)
			} else {
				log.Debug("started VM", "startcommand", startCommand, "error", err, "hypervisor", ip, "output", string(result))
				break
			}
		}
	}
}

func ConfigureNode(ctx context.Context, ip string, configFile string, talosContext string, log *slog.Logger) {
	command := exec.CommandContext(ctx, "talosctl",
		"--nodes", ip,
		"apply-config",
		"--insecure",
		"--file", configFile,
	)
	result, err := command.CombinedOutput()
	log.Debug("run result", "startcommand", command, "error", err, "output", string(result))
}

func BootstrapEtcd(ctx context.Context, talosConfig string, ip string, talosContext string, k8sContext string, log *slog.Logger) error {
	command := exec.CommandContext(ctx, "talosctl",
		"--talosconfig", talosConfig,
		"--context", talosContext,
		"--nodes", ip,
		"--endpoints", ip,
		"bootstrap",
	)

	result, err := command.CombinedOutput()
	log.Debug("run result", "command", command, "error", err, "output", string(result))
	if err != nil {
		log.Error("bootstrap failed", "output", string(result),
			"command", fmt.Sprintf("talosctl --talosconfig %s --context %s --nodes %s --endpoints %s bootstrap",
				talosConfig, talosContext, ip, ip))
		return err
	}

	//Wait for port 6443 and then 50000 in case node is restarting and still coming up
	ports := []int{6443, 50000}
	for _, port := range ports {
		for {
			select {
			case <-ctx.Done():
				return nil
			default:
			}

			conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), time.Second)
			if err != nil {
				log.Debug("unable to connect", "target", fmt.Sprintf("%s:%d", ip, port), "error", err.Error())
				time.Sleep(time.Second)
			} else {
				conn.Close()
				break
			}
		}
	}

	//Fetch the K8S config
	command = exec.CommandContext(ctx, "talosctl",
		"--talosconfig", talosConfig,
		"--context", talosContext,
		"--nodes", ip,
		"--endpoints", ip,
		"kubeconfig",
		"--force",
		"--force-context-name", k8sContext,
	)
	result, err = command.CombinedOutput()
	log.Debug("run result", "command", command, "error", err, "output", string(result))
	if err != nil {
		log.Error("fetching k8s config failed", "output", string(result))
		return err
	}
	return nil
}

func FinalizeInstall(ctx context.Context, log *slog.Logger) error {
	log.Info("finalizing installation with post-install script")
	command := exec.CommandContext(ctx, "bash", "/home/boss/kube/post-install.sh")
	result, err := command.CombinedOutput()
	log.Debug("run result", "command", command, "error", err, "output", string(result))
	if err != nil {
		log.Error("failed to run post-install.sh", "output", string(result))
		return err
	}
	return nil
}

func (i *TalosInitializer) watchPods(ctx context.Context, initStatus *InitializerStatus, log *slog.Logger) {
	// Create Kubernetes client using default kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
	if err != nil {
		log.Error("failed to build kubernetes config", "error", err)
		return
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Error("failed to create kubernetes client", "error", err)
		return
	}

	lastNumPods := 0
	lastInitializedPods := 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Get all pods across all namespaces
		pods, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
		if err != nil {
			log.Error("failed to list pods", "error", err)
			time.Sleep(time.Second)
			continue
		}

		numPods := len(pods.Items)
		initializedPods := 0

		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				initializedPods++
			}
		}

		log.Debug("pod progress", "total", numPods, "running", initializedPods)
		if numPods != lastNumPods || initializedPods != lastInitializedPods {
			initStatus.NumPods = numPods
			initStatus.InitializedPods = initializedPods
			i.sendStatusUpdate(*initStatus)
		}

		lastNumPods = numPods
		lastInitializedPods = initializedPods

		time.Sleep(time.Second)
	}
}
