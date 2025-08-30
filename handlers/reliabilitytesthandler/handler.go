package reliabilitytesthandler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/DRuggeri/labwatch/watchers/common"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type Config struct {
	BaseURL      string         `yaml:"baseUrl"`
	TestInterval int            `yaml:"testInterval"` // seconds between tests
	Timeouts     map[string]int `yaml:"timeouts"`     // timeouts per step in seconds
	AbortSteps   []string       `yaml:"abortSteps"`   // steps that cause shutdown on failure/timeout
}

type StepTiming struct {
	StepName  string    `json:"stepName"`
	StartTime time.Time `json:"startTime"`
	EndTime   time.Time `json:"endTime"`
	Duration  float64   `json:"duration"` // in seconds
	TimedOut  bool      `json:"timedOut"`
	Failed    bool      `json:"failed"`
}

type ReliabilityTestStatus struct {
	// Overall statistics
	TotalSuccesses int            `json:"totalSuccesses"`
	TotalFailures  int            `json:"totalFailures"`
	TotalTests     int            `json:"totalTests"`
	FailuresByStep map[string]int `json:"failuresByStep"`

	IsRunning          bool         `json:"isRunning"`
	CurrentStep        string       `json:"currentStep"`
	CurrentTestStart   time.Time    `json:"currentTestStart"`
	CurrentStepStart   time.Time    `json:"currentStepStart"`
	CurrentStepTimings []StepTiming `json:"currentStepTimings"`

	Config *Config `json:"config"`
}

type ReliabilityTestHandler struct {
	log               *slog.Logger
	config            *Config
	clients           map[string]chan<- ReliabilityTestStatus
	clientsMutex      sync.Mutex
	status            ReliabilityTestStatus
	statusMutex       sync.RWMutex
	upgrader          websocket.Upgrader
	httpClient        *http.Client
	testCtx           context.Context    // Overall test lifecycle context
	testCancel        context.CancelFunc // Cancel function for stopping tests
	currentTestCtx    context.Context    // Context for current test iteration
	currentTestCancel context.CancelFunc // Cancel function for current test completion
	stepTimeoutCancel context.CancelFunc // Cancel function for current step timeout
	abortSteps        map[string]bool
}

func NewReliabilityTestHandler(config *Config, log *slog.Logger) (*ReliabilityTestHandler, error) {
	abortStepsMap := make(map[string]bool)
	for _, step := range config.AbortSteps {
		step = strings.TrimSpace(step)
		if step != "" {
			abortStepsMap[step] = true
		}
	}

	handler := &ReliabilityTestHandler{
		log:        log.With("component", "reliabilityTestHandler"),
		config:     config,
		clients:    make(map[string]chan<- ReliabilityTestStatus),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		abortSteps: abortStepsMap,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
		status: ReliabilityTestStatus{
			FailuresByStep:     make(map[string]int),
			CurrentStepTimings: make([]StepTiming, 0),
			Config:             config,
		},
	}

	return handler, nil
}

// Watch continuously processes status updates from the provided channel
// It always runs to avoid blocking the channel, but only acts on status when a test is running
func (h *ReliabilityTestHandler) Watch(ctx context.Context, statusChan <-chan common.LabStatus) {
	h.log.Info("starting reliability test status watcher")

	for {
		select {
		case <-ctx.Done():
			return
		case status, ok := <-statusChan:
			if !ok {
				h.log.Error("status channel closed")
				return
			}

			// Always consume from channel to prevent blocking, but only process if test is running
			if h.testCtx == nil || h.testCtx.Err() != nil {
				h.log.Debug("test not running, ignoring status update")
				continue
			}

			h.processStatusUpdate(status)
		}
	}
}

func (h *ReliabilityTestHandler) addClient(id string, ch chan<- ReliabilityTestStatus) {
	h.clientsMutex.Lock()
	h.clients[id] = ch
	h.clientsMutex.Unlock()
	h.log.Debug("added reliability test client", "id", id, "totalClients", len(h.clients))
}

func (h *ReliabilityTestHandler) removeClient(id string) {
	h.clientsMutex.Lock()
	delete(h.clients, id)
	clientCount := len(h.clients)
	h.clientsMutex.Unlock()
	h.log.Debug("removed reliability test client", "id", id, "totalClients", clientCount)
}

func (h *ReliabilityTestHandler) broadcastStatus() {
	h.statusMutex.RLock()
	// Create a deep copy of the status to avoid race conditions during JSON marshaling
	status := ReliabilityTestStatus{
		TotalTests:         h.status.TotalTests,
		TotalSuccesses:     h.status.TotalSuccesses,
		TotalFailures:      h.status.TotalFailures,
		IsRunning:          h.status.IsRunning,
		CurrentStep:        h.status.CurrentStep,
		CurrentTestStart:   h.status.CurrentTestStart,
		CurrentStepStart:   h.status.CurrentStepStart,
		CurrentStepTimings: make([]StepTiming, len(h.status.CurrentStepTimings)),
		FailuresByStep:     make(map[string]int),
		Config:             h.status.Config,
	}
	copy(status.CurrentStepTimings, h.status.CurrentStepTimings)
	for k, v := range h.status.FailuresByStep {
		status.FailuresByStep[k] = v
	}
	h.statusMutex.RUnlock()

	h.clientsMutex.Lock()
	if len(h.clients) > 0 {
		h.log.Debug("broadcasting reliability test status", "clients", len(h.clients))

		// Create a copy of clients to avoid holding lock during broadcast
		clientsCopy := make(map[string]chan<- ReliabilityTestStatus, len(h.clients))
		for k, v := range h.clients {
			clientsCopy[k] = v
		}
		h.clientsMutex.Unlock()

		// Broadcast without holding the lock
		for id, ch := range clientsCopy {
			select {
			case ch <- status:
				// Successfully sent
			default:
				// Channel full, skip this client to avoid blocking
				h.log.Warn("reliability test client channel full, skipping", "id", id)
			}
		}
	} else {
		h.clientsMutex.Unlock()
	}
}

func (h *ReliabilityTestHandler) startTest() error {
	if h.testCtx != nil && h.testCtx.Err() == nil {
		return fmt.Errorf("reliability test is already running")
	}

	// Create new context for this test run
	h.testCtx, h.testCancel = context.WithCancel(context.Background())

	h.statusMutex.Lock()
	h.status.IsRunning = true
	h.statusMutex.Unlock()

	h.log.Info("starting reliability test")
	h.broadcastStatus()

	go func() {
		defer func() {
			h.statusMutex.Lock()
			h.status.IsRunning = false
			h.statusMutex.Unlock()

			h.broadcastStatus()
			h.log.Info("reliability test stopped")
		}()

		for {
			select {
			case <-h.testCtx.Done():
				return
			default:
			}

			err := h.runTest()
			if err != nil {
				h.log.Error("test iteration failed", "error", err)
				// Wait a bit before retrying
				time.Sleep(10 * time.Second)
				continue
			}

			// Wait between tests
			select {
			case <-h.testCtx.Done():
				return
			case <-time.After(time.Duration(h.config.TestInterval) * time.Second):
			}
		}
	}()

	return nil
}

func (h *ReliabilityTestHandler) runTest() error {
	h.statusMutex.Lock()
	h.status.CurrentTestStart = time.Now()
	h.status.CurrentStepTimings = make([]StepTiming, 0)
	h.statusMutex.Unlock()

	// Create a new context for this individual test iteration
	h.currentTestCtx, h.currentTestCancel = context.WithCancel(h.testCtx)

	h.log.Info("starting new test iteration")

	if err := h.startLab(); err != nil {
		return fmt.Errorf("failed to start lab: %w", err)
	}

	// Wait for test completion or overall test stop
	select {
	case <-h.testCtx.Done():
		// Overall test run was stopped
		return nil
	case <-h.currentTestCtx.Done():
		// This individual test iteration completed (success or failure)
		return nil
	}
}

func (h *ReliabilityTestHandler) startLab() error {
	h.log.Debug("starting lab test")

	url := fmt.Sprintf("%s/setlab?lab=virtual-2", h.config.BaseURL)
	resp, err := h.httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("failed to start lab: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	h.log.Debug("lab start request sent successfully")
	return nil
}

// processStatusUpdate handles the actual status processing logic
func (h *ReliabilityTestHandler) processStatusUpdate(status common.LabStatus) {
	h.log.Debug("processing status update",
		"currentStep", status.Initializer.CurrentStep,
		"failed", status.Initializer.Failed,
		"labName", status.Initializer.LabName)

	// Check if step changed
	h.statusMutex.Lock()
	if status.Initializer.CurrentStep != h.status.CurrentStep {
		now := time.Now()

		// End previous step timing if exists
		if len(h.status.CurrentStepTimings) > 0 {
			lastTiming := &h.status.CurrentStepTimings[len(h.status.CurrentStepTimings)-1]
			if lastTiming.EndTime.IsZero() {
				lastTiming.EndTime = now
				lastTiming.Duration = lastTiming.EndTime.Sub(lastTiming.StartTime).Seconds()
			}
		}

		// Start new step timing
		lastStep := h.status.CurrentStep
		h.status.CurrentStep = status.Initializer.CurrentStep
		h.status.CurrentStepStart = now
		h.status.CurrentStepTimings = append(h.status.CurrentStepTimings, StepTiming{
			StepName:  status.Initializer.CurrentStep,
			StartTime: now,
		})

		h.log.Info("step changed", "from", lastStep, "to", status.Initializer.CurrentStep)
		h.statusMutex.Unlock()

		h.setStepTimer(status.Initializer.CurrentStep)
		h.broadcastStatus()
	} else {
		h.statusMutex.Unlock()
	}

	// Check for failure
	if status.Initializer.Failed {
		// Check if we've already processed failure for this test
		if h.currentTestCtx == nil || h.currentTestCtx.Err() != nil {
			h.log.Debug("test failure already processed, ignoring duplicate")
			return
		}
		h.log.Warn("test failed", "step", h.status.CurrentStep)
		h.handleStepFailure(h.status.CurrentStep, false)
		return
	}

	// Check for completion
	if status.Initializer.CurrentStep == "done" {
		// Check if we've already processed completion for this test
		// (if currentTestCtx is nil or cancelled, test is already completed)
		if h.currentTestCtx == nil || h.currentTestCtx.Err() != nil {
			h.log.Debug("test completion already processed, ignoring duplicate")
			return
		}

		h.log.Info("test completed successfully")
		h.statusMutex.Lock()
		h.status.TotalSuccesses++
		h.status.TotalTests++

		// End the final step timing
		if len(h.status.CurrentStepTimings) > 0 {
			lastTiming := &h.status.CurrentStepTimings[len(h.status.CurrentStepTimings)-1]
			if lastTiming.EndTime.IsZero() {
				lastTiming.EndTime = time.Now()
				lastTiming.Duration = lastTiming.EndTime.Sub(lastTiming.StartTime).Seconds()
			}
		}
		h.statusMutex.Unlock()

		h.markTestComplete()
	}
}

func (h *ReliabilityTestHandler) setStepTimer(step string) {
	// Cancel existing timeout if any
	if h.stepTimeoutCancel != nil {
		h.stepTimeoutCancel()
	}

	timeout, exists := h.config.Timeouts[step]
	if !exists {
		h.log.Debug("no timeout configured for step", "step", step)
		return
	}

	h.log.Debug("setting timeout for step", "step", step, "timeout", timeout)

	// Use context.WithTimeout for cleaner timeout management
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	h.stepTimeoutCancel = cancel

	// Start timeout goroutine
	go func() {
		<-ctx.Done()
		if ctx.Err() == context.DeadlineExceeded {
			h.log.Warn("step timed out", "step", step, "timeout", timeout)
			h.handleStepFailure(step, true)
		}
		// If context was cancelled (not timed out), do nothing
	}()
}

func (h *ReliabilityTestHandler) clearStepTimer() {
	if h.stepTimeoutCancel != nil {
		h.stepTimeoutCancel()
		h.stepTimeoutCancel = nil
	}
}

func (h *ReliabilityTestHandler) handleStepFailure(step string, timedOut bool) {
	h.log.Debug("handling step failure", "step", step, "timedOut", timedOut)

	h.statusMutex.Lock()
	h.status.TotalFailures++
	h.status.TotalTests++
	h.status.FailuresByStep[step]++

	// Update current step timing
	if len(h.status.CurrentStepTimings) > 0 {
		lastTiming := &h.status.CurrentStepTimings[len(h.status.CurrentStepTimings)-1]
		if lastTiming.StepName == step && lastTiming.EndTime.IsZero() {
			lastTiming.EndTime = time.Now()
			lastTiming.Duration = lastTiming.EndTime.Sub(lastTiming.StartTime).Seconds()
			lastTiming.TimedOut = timedOut
			lastTiming.Failed = true
		}
	}

	// Check if this step should trigger abort
	shouldAbort := h.abortSteps[step]
	h.statusMutex.Unlock()

	h.log.Debug("step failure handling", "step", step, "shouldAbort", shouldAbort)

	if shouldAbort {
		h.log.Warn("aborting test due to failure on critical step", "step", step, "timedOut", timedOut)
		h.stopTest()
	} else {
		h.markTestComplete()
	}
}

func (h *ReliabilityTestHandler) markTestComplete() {
	h.log.Debug("marking test complete")
	h.clearStepTimer()

	h.statusMutex.Lock()
	h.status.CurrentStep = ""
	h.status.CurrentStepStart = time.Time{}
	h.statusMutex.Unlock()

	h.broadcastStatus()

	// Signal test completion by cancelling the current test context
	if h.currentTestCancel != nil {
		h.log.Debug("cancelling current test context")
		h.currentTestCancel()
		h.currentTestCancel = nil
	} else {
		h.log.Warn("currentTestCancel is nil when trying to mark test complete")
	}
}

func (h *ReliabilityTestHandler) stopTest() error {
	// Check if test is running
	if h.testCtx == nil || h.testCtx.Err() != nil {
		return fmt.Errorf("reliability test is not running")
	}

	h.log.Info("stopping reliability test")
	h.testCancel() // Cancel the test context
	h.clearStepTimer()

	return nil
}

func (h *ReliabilityTestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Handle action parameter for starting/stopping tests
	action := r.URL.Query().Get("action")
	if action != "" {
		switch action {
		case "start":
			if err := h.startTest(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Test started"))
			return
		case "stop":
			if err := h.stopTest(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Test stopped"))
			return
		default:
			http.Error(w, "Invalid action. Use 'start' or 'stop'", http.StatusBadRequest)
			return
		}
	}

	// Handle non-WebSocket requests (return current status as JSON)
	if r.Header.Get("Upgrade") == "" {
		h.statusMutex.RLock()
		status := h.status
		h.statusMutex.RUnlock()

		b, _ := json.Marshal(status)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
		return
	}

	// Handle WebSocket upgrade
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Info("websocket upgrade failed", "error", err.Error())
		return
	}
	defer conn.Close()

	// Create channel for this client
	thisChan := make(chan ReliabilityTestStatus, 5) // Buffered to prevent blocking
	clientID := uuid.New().String()

	h.addClient(clientID, thisChan)
	defer h.removeClient(clientID)

	// Send current status immediately
	h.statusMutex.RLock()
	currentStatus := h.status
	h.statusMutex.RUnlock()

	if err := conn.WriteJSON(currentStatus); err != nil {
		h.log.Info("failed to send initial status", "error", err.Error())
		return
	}

	// Listen for status updates and send to client
	for {
		select {
		case <-r.Context().Done():
			return
		case status := <-thisChan:
			if err := conn.WriteJSON(status); err != nil {
				h.log.Debug("client disconnected", "error", err.Error())
				return
			}
		}
	}
}
