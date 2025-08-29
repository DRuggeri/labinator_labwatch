package kubernetes

import (
	"context"
	"log/slog"
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

var reconnectDuration = time.Duration(250) * time.Millisecond
var resyncDuration = time.Duration(30) * time.Second

type KubeWatcher struct {
	configPath string
	clientSet  *kubernetes.Clientset
	namespace  string
	log        *slog.Logger
}

type PodStatus struct {
	PodName   string
	Namespace string
	Status    string
	Node      string
}

func NewKubeWatcher(configPath string, namespace string, log *slog.Logger) (*KubeWatcher, error) {
	if configPath == "" {
		configPath = homedir.HomeDir() + "/.kube/config"
	}

	config, err := clientcmd.BuildConfigFromFlags("", configPath)
	if err != nil {
		return nil, err
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
		return nil, err
	}

	return &KubeWatcher{
		configPath: configPath,
		clientSet:  clientSet,
		namespace:  namespace,
		log:        log,
	}, nil
}

func (w *KubeWatcher) Watch(controlContext context.Context, podChan chan<- map[string]PodStatus) {
	w.log.Info("watching for pod changes", "namespace", w.namespace)
	connected := false
	go func() {
		for {
			select {
			case <-controlContext.Done():
				return
			default:
				// Not ready to read from control channel - carry on
			}

			v, err := w.clientSet.Discovery().ServerVersion()
			if err != nil {
				// The client starts attempting to connect at the start of the lab, but it'll be a while before
				// we can actually connect. Keep the logs quiet until we connect at least once
				if connected {
					w.log.Error("error connecting to kubernetes", "error", err)
				}
				time.Sleep(reconnectDuration)
				continue
			}
			connected = true

			status := make(map[string]PodStatus)
			w.log.Info("connected to Kubernetes", "version", v.String())
			opts := []informers.SharedInformerOption{
				informers.WithTweakListOptions(func(opt *metav1.ListOptions) { opt.FieldSelector = fields.Everything().String() }),
			}
			if w.namespace != "" {
				opts = append(opts, informers.WithNamespace(w.namespace))
			}

			factory := informers.NewSharedInformerFactoryWithOptions(w.clientSet, resyncDuration, opts...)

			informer := factory.Core().V1().Pods().Informer()

			informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					pod := obj.(*corev1.Pod)
					p := podToStatus(pod)
					w.log.Debug("pod added", "name", p.PodName, "node", p.Node, "status", p.Status)
					status[p.PodName] = p
					// Create a deep copy to avoid concurrent map access
					statusCopy := make(map[string]PodStatus, len(status))
					for key, value := range status {
						statusCopy[key] = value
					}
					podChan <- statusCopy
				},
				UpdateFunc: func(oldObj, newObj interface{}) {
					pod := newObj.(*corev1.Pod)
					p := podToStatus(pod)
					if o, ok := status[p.PodName]; !ok || o.Status != p.Status || o.Node != p.Node {
						w.log.Debug("pod updated", "name", p.PodName, "node", p.Node, "status", p.Status)
						status[p.PodName] = p
						// Create a deep copy to avoid concurrent map access
						statusCopy := make(map[string]PodStatus, len(status))
						for key, value := range status {
							statusCopy[key] = value
						}
						podChan <- statusCopy
					}
				},
				DeleteFunc: func(obj interface{}) {
					pod := obj.(*corev1.Pod)
					p := podToStatus(pod)
					w.log.Debug("pod deleted", "name", p.PodName, "node", p.Node, "status", p.Status)
					delete(status, p.PodName)
					// Create a deep copy to avoid concurrent map access
					statusCopy := make(map[string]PodStatus, len(status))
					for key, value := range status {
						statusCopy[key] = value
					}
					podChan <- statusCopy
				},
			})

			go factory.Start(controlContext.Done())
			factory.WaitForCacheSync(controlContext.Done())
			w.log.Info("cache for cluster synchronized")

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
