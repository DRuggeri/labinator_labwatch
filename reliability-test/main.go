package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/DRuggeri/labwatch/watchers/common"
	"github.com/gorilla/websocket"
)

type ReliabilityTest struct {
	testResults        map[string]int
	config             *Config
	totalTests         int
	successCount       int
	failureCount       int
	currentStep        string
	lastStepChangeTime time.Time
	testStartTime      time.Time
	stepTimer          *time.Timer
	stepTimerCancel    context.CancelFunc
	log                *slog.Logger
	wsConn             *websocket.Conn
	httpClient         *http.Client
	wsURL              string
	shutdownRequested  bool
}

func NewReliabilityTest(config *Config, log *slog.Logger) *ReliabilityTest {
	// Parse the base URL to construct WebSocket URL
	u, err := url.Parse(config.BaseURL)
	if err != nil {
		log.Error("invalid base URL", "url", config.BaseURL, "error", err)
		os.Exit(1)
	}

	// Convert HTTP to WebSocket scheme
	wsScheme := "ws"
	if u.Scheme == "https" {
		wsScheme = "wss"
	}

	wsURL := fmt.Sprintf("%s://%s/status", wsScheme, u.Host)

	return &ReliabilityTest{
		testResults:        make(map[string]int),
		config:             config,
		log:                log,
		httpClient:         &http.Client{Timeout: 30 * time.Second},
		wsURL:              wsURL,
		lastStepChangeTime: time.Now(),
	}
}

func (rt *ReliabilityTest) startLab() error {
	rt.log.Debug("starting lab test")

	url := fmt.Sprintf("%s/setlab?lab=virtual-2", rt.config.BaseURL)
	resp, err := rt.httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("failed to start lab: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	rt.log.Debug("lab start request sent successfully")
	return nil
}

func (rt *ReliabilityTest) connectWebSocket() error {
	rt.log.Debug("connecting to WebSocket", "url", rt.wsURL)

	conn, _, err := websocket.DefaultDialer.Dial(rt.wsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}

	rt.wsConn = conn
	rt.log.Debug("WebSocket connected successfully")
	return nil
}

func (rt *ReliabilityTest) setStepTimer(step string) {
	// Cancel existing timer if any
	if rt.stepTimerCancel != nil {
		rt.stepTimerCancel()
	}

	timeout, exists := rt.config.Timeouts[step]
	if !exists {
		rt.log.Debug("no timeout configured for step", "step", step)
		return
	}

	rt.log.Debug("setting timeout for step", "step", step, "timeout", timeout)

	ctx, cancel := context.WithCancel(context.Background())
	rt.stepTimerCancel = cancel

	rt.stepTimer = time.AfterFunc(time.Duration(timeout)*time.Second, func() {
		select {
		case <-ctx.Done():
			// Timer was cancelled
			return
		default:
			rt.log.Warn("step timed out", "step", step, "timeout", timeout)
			rt.testResults[step]++
			rt.failureCount++
			rt.markTestComplete()
		}
	})
}

func (rt *ReliabilityTest) clearStepTimer() {
	if rt.stepTimerCancel != nil {
		rt.stepTimerCancel()
		rt.stepTimerCancel = nil
	}
	if rt.stepTimer != nil {
		rt.stepTimer.Stop()
		rt.stepTimer = nil
	}
}

func (rt *ReliabilityTest) markTestComplete() {
	rt.clearStepTimer()
	rt.totalTests++
	rt.currentStep = ""
	rt.lastStepChangeTime = time.Now()

	if rt.wsConn != nil {
		rt.wsConn.Close()
		rt.wsConn = nil
	}
}

func (rt *ReliabilityTest) runTest() (time.Duration, error) {
	rt.testStartTime = time.Now()
	rt.log.Info("starting new test iteration")
	rt.lastStepChangeTime = rt.testStartTime // Reset step change time for this test

	// Start the lab
	if err := rt.startLab(); err != nil {
		return time.Since(rt.testStartTime), fmt.Errorf("failed to start lab: %w", err)
	}

	// Connect to WebSocket
	if err := rt.connectWebSocket(); err != nil {
		return time.Since(rt.testStartTime), fmt.Errorf("failed to connect WebSocket: %w", err)
	}

	// Read status updates
	for {
		if rt.shutdownRequested {
			return time.Since(rt.testStartTime), nil
		}

		var status common.LabStatus
		err := rt.wsConn.ReadJSON(&status)
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				rt.log.Debug("WebSocket closed normally")
				break
			}
			return time.Since(rt.testStartTime), fmt.Errorf("failed to read WebSocket message: %w", err)
		}

		rt.log.Debug("received status update",
			"currentStep", status.Initializer.CurrentStep,
			"failed", status.Initializer.Failed,
			"labName", status.Initializer.LabName)

		// Check if step changed
		if status.Initializer.CurrentStep != rt.currentStep {
			now := time.Now()
			elapsed := now.Sub(rt.lastStepChangeTime)
			timeout, hasTimeout := rt.config.Timeouts[status.Initializer.CurrentStep]

			logFields := []any{
				"from", rt.currentStep,
				"to", status.Initializer.CurrentStep,
				"elapsed", elapsed.Round(time.Millisecond),
			}

			if hasTimeout {
				logFields = append(logFields, "timeout", fmt.Sprintf("%ds", timeout))
			} else {
				logFields = append(logFields, "timeout", "none")
			}

			rt.log.Info("step changed", logFields...)

			rt.currentStep = status.Initializer.CurrentStep
			rt.lastStepChangeTime = now
			rt.setStepTimer(rt.currentStep)
		}

		// Check for failure
		if status.Initializer.Failed {
			rt.log.Warn("test failed", "step", rt.currentStep)
			rt.testResults[rt.currentStep]++
			rt.failureCount++
			rt.markTestComplete()
			break
		}

		// Check for completion
		if status.Initializer.CurrentStep == "done" {
			rt.log.Info("test completed successfully")
			rt.testResults["done"]++
			rt.successCount++
			rt.markTestComplete()
			break
		}
	}

	return time.Since(rt.testStartTime), nil
}

func (rt *ReliabilityTest) printRunningCounts() {
	if rt.totalTests > 0 {
		successRate := float64(rt.successCount) / float64(rt.totalTests) * 100
		fmt.Printf("Running totals: %d tests completed | %d successes | %d failures | %.1f%% success rate\n",
			rt.totalTests, rt.successCount, rt.failureCount, successRate)
	}
}

func (rt *ReliabilityTest) printResults() {
	fmt.Println("\n=== RELIABILITY TEST RESULTS ===")
	fmt.Printf("Total tests run: %d\n", rt.totalTests)
	fmt.Println("\nResults by step:")

	// Get sorted keys for consistent output
	keys := make([]string, 0, len(rt.testResults))
	for k := range rt.testResults {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, step := range keys {
		count := rt.testResults[step]
		fmt.Printf("  %s: %d\n", step, count)
	}

	// Calculate success rate using tracked counts
	if rt.totalTests > 0 {
		successRate := float64(rt.successCount) / float64(rt.totalTests) * 100
		fmt.Printf("\nFinal Results:\n")
		fmt.Printf("  Successes: %d\n", rt.successCount)
		fmt.Printf("  Failures: %d\n", rt.failureCount)
		fmt.Printf("  Success rate: %.2f%% (%d/%d)\n", successRate, rt.successCount, rt.totalTests)
	}
}

func (rt *ReliabilityTest) Run() {
	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		rt.log.Info("shutdown signal received")
		rt.shutdownRequested = true
		rt.clearStepTimer()
		if rt.wsConn != nil {
			rt.wsConn.Close()
		}
	}()

	rt.log.Info("starting reliability test loop")

	for !rt.shutdownRequested {
		duration, err := rt.runTest()
		if err != nil {
			rt.log.Error("test iteration failed", "duration", fmt.Sprintf("%.1fs", duration.Seconds()), "error", err)
			// Wait a bit before retrying
			time.Sleep(10 * time.Second)
			continue
		} else {
			rt.log.Info("test iteration succeeded", "duration", fmt.Sprintf("%.1fs", duration.Seconds()))
		}

		// Print running counts after each test
		rt.printRunningCounts()

		if !rt.shutdownRequested {
			// Wait between tests
			rt.log.Info("waiting before next test", "totalTests", rt.totalTests)
			time.Sleep(time.Duration(rt.config.TestInterval) * time.Second)
		}
	}

	rt.printResults()
	rt.log.Info("reliability test completed")
}

func main() {
	// Parse command line flags
	var configFile string
	flag.StringVar(&configFile, "config", "", "path to configuration file")
	flag.Parse()

	// Set up logging
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Load configuration
	config, err := LoadConfig(configFile)
	if err != nil {
		log.Warn("failed to load config file, using defaults", "file", configFile, "error", err)
	}

	// Override with environment variable if set
	if envURL := os.Getenv("LABWATCH_URL"); envURL != "" {
		config.BaseURL = envURL
	}

	log.Info("starting reliability test", "baseURL", config.BaseURL, "testInterval", config.TestInterval)

	// Create and run the reliability test
	rt := NewReliabilityTest(config, log)
	rt.Run()
}
