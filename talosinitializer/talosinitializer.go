package talosinitializer

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/DRuggeri/labwatch/watchers/port"
	"github.com/DRuggeri/labwatch/watchers/talos"
	"gopkg.in/yaml.v3"
)

type TalosInitializer struct {
	config     ScenarioConfigs
	nodesDir   string
	log        *slog.Logger
	k8sContext string
}

type ScenarioConfigs struct {
	Scenarios map[string]ScenarioConfig
}

type ScenarioConfig struct {
	Nodes map[string]NodeConfig
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

func NewTalosInitializer(configFile string, scenarioNodesDir string, k8sContext string, log *slog.Logger) (*TalosInitializer, error) {
	d, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}

	cfg := ScenarioConfigs{}
	err = yaml.Unmarshal(d, &cfg)
	if err != nil {
		return nil, err
	}

	// Add in node names from key for ease of reference later
	for scenarioName, scenarioCfg := range cfg.Scenarios {
		for name, nodeCfg := range scenarioCfg.Nodes {
			nodeCfg.Name = name
			nodeCfg.configFile = filepath.Join(scenarioNodesDir, scenarioName, fmt.Sprintf("node-%s.yaml", name))

			//Ensure config file can be read
			if _, err := os.ReadFile(nodeCfg.configFile); err != nil {
				return nil, fmt.Errorf("failed to read config file for node %s: %w", nodeCfg.Name, err)
			}
		}
	}

	return &TalosInitializer{
		config:     cfg,
		nodesDir:   scenarioNodesDir,
		k8sContext: k8sContext,
		log:        log.With("operation", "TalosInitializer"),
	}, nil
}

func (i *TalosInitializer) Initialize(scenario string, controlContext context.Context, portStatuses <-chan port.PortStatus, talosStatuses <-chan talos.NodeStatus) {
	config, ok := i.config.Scenarios[scenario]
	log := i.log.With("operation", fmt.Sprintf("TalosInitializer-%s", scenario))
	if !ok {
		log.Error("scenario does not exist")
	}

	hypervisors, nodeNames := GetHypervisorsAndNodes(config)
	nodeEndpointMap := map[string]NodeConfig{}
	hypervisorEndpointMap := map[string]string{}

	for ip := range hypervisors {
		hypervisorEndpointMap[fmt.Sprintf("%s:22", ip)] = ip
	}

	for _, name := range nodeNames {
		nodeEndpointMap[fmt.Sprintf("%s:50000", config.Nodes[name].IP)] = config.Nodes[name]
	}

	currentStatus := port.PortStatus{}
INITLOOP:
	for {
		select {
		case <-controlContext.Done():
			return
		case s := <-portStatuses:
			endpointsUp := []string{}

			// Get list of ports that came up
			for endpoint, up := range s {
				if !currentStatus[endpoint] && up {
					endpointsUp = append(endpointsUp, endpoint)
					log.Info("endpoint has come up", "endpoint", endpoint)
				}
			}

			// Take the next action on the port
			for _, endpoint := range endpointsUp {
				if ip, ok := hypervisorEndpointMap[endpoint]; ok {
					log.Info(fmt.Sprintf("hypervisor is up - starting %d VMs", len(hypervisors[ip])), "ip", ip)
					delete(hypervisorEndpointMap, endpoint)
					StartVMsOnHypervisor(endpoint, hypervisors[ip], log)

				} else if node, ok := nodeEndpointMap[endpoint]; ok {
					log.Info("node is up", "name", node.Name)
					delete(nodeEndpointMap, endpoint)
					//ConfigureNode(node.IP, node.configFile, scenario, log)
				} else {
					log.Error("discarding port up information because it is not a known hypervisor or node", "endpoint", endpoint)
				}
			}

			// Update statuses
			currentStatus = s

			// All done initializing stuff!
			if len(hypervisorEndpointMap) == 0 && len(nodeEndpointMap) == 0 {
				break INITLOOP
			}
			log.Debug("initializations to complete", "hypervisors", len(hypervisorEndpointMap), "nodes", len(nodeEndpointMap))
		}
	}

	// All the nodes have been reconfigured so we can now bootstrap via any control plane node
	for name, node := range config.Nodes {
		if node.Role == "controlplane" {
			log.Info("bootstrapping Kubernetes through control plane node", "node", name)
			BootstrapCluster(node.IP, scenario, i.k8sContext, log)
			break
		}
	}
}

func GetHypervisorsAndNodes(config ScenarioConfig) (map[string][]NodeConfig, []string) {
	hypervisors := map[string][]NodeConfig{}
	nodes := []string{}
	for _, cfg := range config.Nodes {
		if cfg.Hypervisor.IP != "" {
			if hypervisors[cfg.Hypervisor.IP] == nil {
				hypervisors[cfg.Hypervisor.IP] = []NodeConfig{}
			}

			hypervisors[cfg.Hypervisor.IP] = append(hypervisors[cfg.Hypervisor.IP], cfg)
		}

		if cfg.Role != "hypervisor" {
			nodes = append(nodes, cfg.Name)
		}
	}

	return hypervisors, nodes
}

func StartVMsOnHypervisor(ip string, nodes []NodeConfig, log *slog.Logger) {
	for i, node := range nodes {
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
		log.Debug("run result", "startcommand", startCommand, "error", err, "output", string(result))
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

func BootstrapCluster(ip string, talosContext string, k8sContext string, log *slog.Logger) {
	command := exec.Command("talosctl",
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
