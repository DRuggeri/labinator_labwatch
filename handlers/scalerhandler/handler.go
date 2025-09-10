package scalerhandler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/client-go/util/homedir"
)

type ScalerHandler struct {
	clientSet      *kubernetes.Clientset
	configPath     string
	namespace      string
	log            *slog.Logger
	lastConfigTime time.Time
}

type ScaleRequest struct {
	Load      string `json:"load"`      // "client" or "server"
	Operation string `json:"operation"` // "add" or "sub"
	Number    int32  `json:"number"`    // number to add/sub
}

type ScaleResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	Deployment  string `json:"deployment"`
	OldReplicas int32  `json:"old_replicas"`
	NewReplicas int32  `json:"new_replicas"`
}

func NewScalerHandler(configPath string, namespace string, log *slog.Logger) (*ScalerHandler, error) {
	if configPath == "" {
		configPath = homedir.HomeDir() + "/.kube/config"
	}

	if namespace == "" {
		namespace = "default"
	}

	h := &ScalerHandler{
		configPath: configPath,
		namespace:  namespace,
		log:        log.With("operation", "scalerhandler"),
	}

	// Initial client setup
	clientSet, modTime, err := h.buildClientSet()
	if err != nil {
		return nil, err
	}

	h.clientSet = clientSet
	h.lastConfigTime = modTime

	return h, nil
}

func (h *ScalerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Parse URL parameters
	load := r.URL.Query().Get("load")
	operation := r.URL.Query().Get("operation")
	numberStr := r.URL.Query().Get("number")

	// Validate parameters
	if load == "" || operation == "" || numberStr == "" {
		h.respondWithError(w, http.StatusBadRequest, "Missing required parameters: load, operation, number")
		return
	}

	if load != "client" && load != "server" {
		h.respondWithError(w, http.StatusBadRequest, "load must be either 'client' or 'server'")
		return
	}

	if operation != "add" && operation != "sub" {
		h.respondWithError(w, http.StatusBadRequest, "operation must be either 'add' or 'sub'")
		return
	}

	number, err := strconv.Atoi(numberStr)
	if err != nil || number < 0 {
		h.respondWithError(w, http.StatusBadRequest, "number must be a non-negative integer")
		return
	}

	// Check if config file has changed and reload if necessary
	if fileInfo, err := os.Stat(h.configPath); err == nil {
		if fileInfo.ModTime().After(h.lastConfigTime) {
			h.log.Debug("kubernetes config file changed, reloading client")
			if newClientSet, newModTime, err := h.buildClientSet(); err == nil {
				h.clientSet = newClientSet
				h.lastConfigTime = newModTime
			} else {
				h.log.Error("failed to reload kubernetes client", "error", err)
			}
		}
	}

	// Determine deployment name
	var deploymentName string
	switch load {
	case "client":
		deploymentName = "counterclient"
	case "server":
		deploymentName = "counterserver"
	}

	// Scale the deployment
	response := h.scaleDeployment(deploymentName, operation, int32(number))

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

func (h *ScalerHandler) scaleDeployment(deploymentName, operation string, number int32) ScaleResponse {
	ctx := context.Background()

	// Get current deployment
	deployment, err := h.clientSet.AppsV1().Deployments(h.namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		h.log.Error("failed to get deployment", "deployment", deploymentName, "error", err)
		return ScaleResponse{
			Success:    false,
			Message:    fmt.Sprintf("Failed to get deployment %s: %v", deploymentName, err),
			Deployment: deploymentName,
		}
	}

	oldReplicas := *deployment.Spec.Replicas
	var newReplicas int32

	switch operation {
	case "add":
		newReplicas = oldReplicas + number
	case "sub":
		newReplicas = oldReplicas - number
		if newReplicas < 0 {
			newReplicas = 0
		}
	}

	h.log.Info("scaling deployment",
		"deployment", deploymentName,
		"operation", operation,
		"number", number,
		"old_replicas", oldReplicas,
		"new_replicas", newReplicas)

	// Update deployment
	deployment.Spec.Replicas = &newReplicas
	_, err = h.clientSet.AppsV1().Deployments(h.namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		h.log.Error("failed to update deployment", "deployment", deploymentName, "error", err)
		return ScaleResponse{
			Success:     false,
			Message:     fmt.Sprintf("Failed to update deployment %s: %v", deploymentName, err),
			Deployment:  deploymentName,
			OldReplicas: oldReplicas,
		}
	}

	return ScaleResponse{
		Success:     true,
		Message:     fmt.Sprintf("Successfully scaled %s from %d to %d replicas", deploymentName, oldReplicas, newReplicas),
		Deployment:  deploymentName,
		OldReplicas: oldReplicas,
		NewReplicas: newReplicas,
	}
}

func (h *ScalerHandler) respondWithError(w http.ResponseWriter, statusCode int, message string) {
	h.log.Error("scaler handler error", "status", statusCode, "message", message)
	w.WriteHeader(statusCode)
	response := ScaleResponse{
		Success: false,
		Message: message,
	}
	json.NewEncoder(w).Encode(response)
}

// buildClientSet creates a new Kubernetes clientset from the config file
func (h *ScalerHandler) buildClientSet() (*kubernetes.Clientset, time.Time, error) {
	fileInfo, err := os.Stat(h.configPath)
	if err != nil {
		return nil, time.Time{}, err
	}
	modTime := fileInfo.ModTime()

	config, err := clientcmd.BuildConfigFromFlags("", h.configPath)
	if err != nil {
		return nil, modTime, err
	}

	config.QPS = -1   // Disable rate limiting
	config.Burst = -1 // Disable burst limiting

	config.Timeout = 10 * time.Second

	config.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()

	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, modTime, err
	}

	return clientSet, modTime, nil
}
