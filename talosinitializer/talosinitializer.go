package talosinitializer

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/DRuggeri/labwatch/watchers/port"
	"gopkg.in/yaml.v3"
)

type TalosInitializer struct {
	talosConfig    string
	scenarioConfig map[string]ScenarioConfig
	nodesDir       string
	log            *slog.Logger
	k8sContext     string
	portStatuses   chan port.PortStatus
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

			//Ensure config file can be read
			if _, err := os.ReadFile(nodeCfg.configFile); err != nil {
				return nil, fmt.Errorf("failed to read config file for node %s: %w", nodeCfg.Name, err)
			}
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
		k8sContext:     k8sContext,
		log:            log.With("operation", "TalosInitializer"),
		portStatuses:   make(chan port.PortStatus),
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

func (i *TalosInitializer) GetPortStatusChan() chan<- port.PortStatus {
	return i.portStatuses
}

func (i *TalosInitializer) Initialize(scenario string, controlContext context.Context) {
	config, ok := i.scenarioConfig[scenario]
	log := i.log.With("operation", fmt.Sprintf("TalosInitializer-%s", scenario))
	if !ok {
		log.Error("scenario does not exist")
	}

	hypervisors, nodeNames := config.GetHypervisorsAndNodes()
	nodeEndpointMap := map[string]NodeConfig{}
	hypervisorEndpointMap := map[string]string{}

	for ip := range hypervisors {
		hypervisorEndpointMap[fmt.Sprintf("%s:22", ip)] = ip
	}

	for _, name := range nodeNames {
		nodeEndpointMap[fmt.Sprintf("%s:50000", config.Nodes[name].IP)] = config.Nodes[name]
	}

	log.Debug("entering initialization loop")
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
					go StartVMsOnHypervisor(hypervisorAddr, hypervisors[ip], log)

				} else if node, ok := nodeEndpointMap[endpoint]; ok {
					log.Info("node is up", "name", node.Name)
					delete(nodeEndpointMap, endpoint)
					//ConfigureNode(node.IP, node.configFile, scenario, log)
				} else {
					log.Error("discarding port up information because it is not a known hypervisor or node", "endpoint", endpoint)
				}
			}

			// Copy statuses
			currentStatus = statusCopy

			// All done initializing stuff!
			if len(hypervisorEndpointMap) == 0 && len(nodeEndpointMap) == 0 {
				break INITLOOP
			}
			log.Debug("initializations to complete", "hypervisors", len(hypervisorEndpointMap), "nodes", len(nodeEndpointMap))
		}
	}
	log.Debug("completed initialization loop")

	// All the nodes have been reconfigured so we can now bootstrap via any control plane node
	for name, node := range config.Nodes {
		if node.Role == "controlplane" {
			log.Info("bootstrapping Kubernetes through control plane node", "node", name)
			BootstrapCluster(i.talosConfig, node.IP, scenario, i.k8sContext, log)
			break
		}
	}
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

func StartVMsOnHypervisor(ip string, nodes []NodeConfig, log *slog.Logger) {
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
				"              --memory 2048 --vcpus 2 --os-variant ubuntu22.10 --virt-type kvm" +
				"              --boot network --noautoconsole --import"

			command := exec.Command("ssh",
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				fmt.Sprintf("root@%s", ip),
				startCommand,
			)

			result, err := command.CombinedOutput()
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

func ConfigureNode(ip string, configFile string, talosContext string, log *slog.Logger) {
	command := exec.Command("talosctl",
		"--nodes", ip,
		"apply-config",
		"--insecure",
		"--file", configFile,
	)
	result, err := command.CombinedOutput()
	log.Debug("run result", "startcommand", command, "error", err, "output", string(result))
}

func BootstrapCluster(talosConfig string, ip string, talosContext string, k8sContext string, log *slog.Logger) {
	command := exec.Command("talosctl",
		"--talosconfig", talosConfig,
		"--context", talosContext,
		"--nodes", ip,
		"--endpoints", ip,
		"bootstrap",
	)

	result, err := command.CombinedOutput()
	log.Debug("run result", "command", command, "error", err, "output", string(result))
	if err != nil {
		log.Error("bootstrap failed", "output", string(result))
		return
	}

	//Wait for port 6443 in case node is restarting and still coming up
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:6443", ip), time.Second)
		if err != nil {
			time.Sleep(time.Second)
		} else {
			conn.Close()
			break
		}
	}

	//Fetch the K8S config
	command = exec.Command("talosctl",
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
		log.Error("fetching k8s config filed", "output", string(result))
		return
	}
}
