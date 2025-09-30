package kubernetes

import (
	"context"
	"log/slog"
	"maps"
	"os"
	"time"

	"k8s.io/client-go/informers"  // Used to create shared informers
	"k8s.io/client-go/kubernetes" // The core client-go package that provides the Clientset

	// Used for in-cluster config
	"k8s.io/client-go/tools/clientcmd" // Used for loading kubeconfig files (out-of-cluster)
	"k8s.io/client-go/util/homedir"    // Utility to find user's home directory

	corev1 "k8s.io/api/core/v1" // Kubernetes Pod API type definition
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/flowcontrol"
)

var reconnectDuration = time.Duration(5) * time.Second
var resyncDuration = time.Duration(30) * time.Second

type KubeWatcher struct {
	configPath     string
	clientSet      *kubernetes.Clientset
	namespace      string
	log            *slog.Logger
	lastConfigTime time.Time
}

type PodStatus struct {
	PodName   string `json:"podName"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
	Node      string `json:"node"`
}

type NodeStatus struct {
	NodeName string   `json:"nodeName"`
	Status   string   `json:"status"`
	Ready    bool     `json:"ready"`
	Version  string   `json:"version"`
	Roles    []string `json:"roles"`
}

type KubeStatus struct {
	Pods  map[string]PodStatus  `json:"pods"`
	Nodes map[string]NodeStatus `json:"nodes"`
}

func NewKubeWatcher(configPath string, namespace string, log *slog.Logger) (*KubeWatcher, error) {
	if configPath == "" {
		configPath = homedir.HomeDir() + "/.kube/config"
	}

	w := &KubeWatcher{
		configPath: configPath,
		namespace:  namespace,
		log:        log.With("operation", "kubewatcher"),
	}

	// Initial client setup
	clientSet, modTime, err := w.buildClientSet()
	if err != nil {
		return nil, err
	}

	w.clientSet = clientSet
	w.lastConfigTime = modTime

	return w, nil
}

func (w *KubeWatcher) Watch(controlContext context.Context, kubeChan chan<- KubeStatus) {
	w.log.Info("watching for pod and node changes", "namespace", w.namespace)
	connected := false
	go func() {
		for {
			select {
			case <-controlContext.Done():
				return
			default:
				// Not ready to read from control channel - carry on
			}

			// Check if config file has changed and reload if necessary
			if fileInfo, err := os.Stat(w.configPath); err == nil {
				if fileInfo.ModTime().After(w.lastConfigTime) {
					w.log.Debug("kubernetes config file changed, reloading client")
					if newClientSet, newModTime, err := w.buildClientSet(); err == nil {
						w.clientSet = newClientSet
						w.lastConfigTime = newModTime
					} else {
						w.log.Error("failed to reload kubernetes client", "error", err)
					}
				}
			}

			v, err := w.clientSet.Discovery().ServerVersion()
			if err != nil {
				// The client will attempt connecting often - suppress errors
				// until it actually connects once
				if connected {
					w.log.Error("error connecting to kubernetes", "error", err)
					connected = false
				}
				time.Sleep(reconnectDuration)
				continue
			}
			connected = true

			kubeStatus := KubeStatus{
				Pods:  make(map[string]PodStatus),
				Nodes: make(map[string]NodeStatus),
			}
			w.log.Info("connected to Kubernetes", "version", v.String())
			opts := []informers.SharedInformerOption{
				informers.WithTweakListOptions(func(opt *metav1.ListOptions) { opt.FieldSelector = fields.Everything().String() }),
			}
			if w.namespace != "" {
				opts = append(opts, informers.WithNamespace(w.namespace))
			}

			factory := informers.NewSharedInformerFactoryWithOptions(w.clientSet, resyncDuration, opts...)

			sendStatus := func() {
				// Create a deep copy to avoid concurrent map access
				statusCopy := KubeStatus{
					Pods:  maps.Clone(kubeStatus.Pods),
					Nodes: maps.Clone(kubeStatus.Nodes),
				}
				kubeChan <- statusCopy
			}

			// Pod informer
			podInformer := factory.Core().V1().Pods().Informer()
			podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					pod := obj.(*corev1.Pod)
					p := podToStatus(pod)
					w.log.Debug("pod added", "name", p.PodName, "node", p.Node, "status", p.Status)
					kubeStatus.Pods[p.PodName] = p
					sendStatus()
				},
				UpdateFunc: func(oldObj, newObj interface{}) {
					pod := newObj.(*corev1.Pod)
					p := podToStatus(pod)
					if o, ok := kubeStatus.Pods[p.PodName]; !ok || o.Status != p.Status || o.Node != p.Node {
						w.log.Debug("pod updated", "name", p.PodName, "node", p.Node, "status", p.Status)
						kubeStatus.Pods[p.PodName] = p
						sendStatus()
					}
				},
				DeleteFunc: func(obj interface{}) {
					pod := obj.(*corev1.Pod)
					p := podToStatus(pod)
					w.log.Debug("pod deleted", "name", p.PodName, "node", p.Node, "status", p.Status)
					delete(kubeStatus.Pods, p.PodName)
					sendStatus()
				},
			})

			// Node informer
			nodeInformer := factory.Core().V1().Nodes().Informer()
			nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					node := obj.(*corev1.Node)
					n := nodeToStatus(node)
					w.log.Debug("node added", "name", n.NodeName, "status", n.Status, "ready", n.Ready)
					kubeStatus.Nodes[n.NodeName] = n
					sendStatus()
				},
				UpdateFunc: func(oldObj, newObj interface{}) {
					node := newObj.(*corev1.Node)
					n := nodeToStatus(node)
					if o, ok := kubeStatus.Nodes[n.NodeName]; !ok || o.Status != n.Status || o.Ready != n.Ready {
						w.log.Debug("node updated", "name", n.NodeName, "status", n.Status, "ready", n.Ready)
						kubeStatus.Nodes[n.NodeName] = n
						sendStatus()
					}
				},
				DeleteFunc: func(obj interface{}) {
					node := obj.(*corev1.Node)
					n := nodeToStatus(node)
					w.log.Debug("node deleted", "name", n.NodeName, "status", n.Status, "ready", n.Ready)
					delete(kubeStatus.Nodes, n.NodeName)
					sendStatus()
				},
			})

			go factory.Start(controlContext.Done())
			factory.WaitForCacheSync(controlContext.Done())
			w.log.Debug("cache for cluster synchronized")

			// This line keeps the goroutine running indefinitely.
			// It will block until the 'stopCh' channel is closed, allowing the informer to run in the background.
			// If `stopCh` is closed, this goroutine will exit.
			<-controlContext.Done()
		}
	}()
}

func podToStatus(pod *corev1.Pod) PodStatus {
	return PodStatus{
		PodName:   pod.GetName(),
		Namespace: pod.GetNamespace(),
		Status:    string(pod.Status.Phase),
		Node:      pod.Spec.NodeName,
	}
}

func nodeToStatus(node *corev1.Node) NodeStatus {
	// Determine if node is ready
	ready := false
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			ready = true
			break
		}
	}

	// Extract node roles from labels
	roles := []string{}
	for label := range node.Labels {
		if label == "node-role.kubernetes.io/master" || label == "node-role.kubernetes.io/control-plane" {
			roles = append(roles, "control-plane")
		} else if label == "node-role.kubernetes.io/worker" {
			roles = append(roles, "worker")
		}
	}
	if len(roles) == 0 {
		roles = append(roles, "worker") // Default to worker if no role labels
	}

	// Get overall node status
	status := "Unknown"
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			if condition.Status == corev1.ConditionTrue {
				status = "Ready"
			} else {
				status = "NotReady"
			}
			break
		}
	}

	return NodeStatus{
		NodeName: node.GetName(),
		Status:   status,
		Ready:    ready,
		Version:  node.Status.NodeInfo.KubeletVersion,
		Roles:    roles,
	}
}

// buildClientSet creates a new Kubernetes clientset from the config file
func (w *KubeWatcher) buildClientSet() (*kubernetes.Clientset, time.Time, error) {
	// Get the modification time of the config file
	fileInfo, err := os.Stat(w.configPath)
	if err != nil {
		return nil, time.Time{}, err
	}
	modTime := fileInfo.ModTime()

	config, err := clientcmd.BuildConfigFromFlags("", w.configPath)
	if err != nil {
		return nil, modTime, err
	}

	// Disable client-side backoff and rate limiting to ensure immediate retries
	config.QPS = -1   // Disable rate limiting
	config.Burst = -1 // Disable burst limiting

	// Set a very short timeout to fail fast and let our reconnect logic handle retries
	config.Timeout = 1 * time.Second

	// Disable the default backoff manager and use a no-op rate limiter
	config.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()

	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, modTime, err
	}

	return clientSet, modTime, nil
}
