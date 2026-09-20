package hivemind

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrNoRelayAvailable     = errors.New("no relay available")
	ErrAuthenticationFailed = errors.New("authentication failed")
	ErrFrameTooLarge        = errors.New("frame too large")
	ErrRelayUnhealthy       = errors.New("relay unhealthy")
	ErrDuplicateFrame       = errors.New("duplicate frame")
	ErrOrderingViolation    = errors.New("ordering violation")
)

// RelayConfig holds relay configuration
type RelayConfig struct {
	PrimaryURL          string
	FallbackURLs        []string
	AuthToken           string
	HMACKey             []byte
	Timeout             time.Duration
	MaxFrameSize        int
	ReplayWindow        time.Duration
	HealthCheckInterval time.Duration
	UnhealthyThreshold  int
	OrderedDelivery     bool
	ReplayFrom          int64
	BackoffBase         time.Duration
	BackoffMax          time.Duration
	BackoffMultiplier   float64
}

func DefaultRelayConfig() RelayConfig {
	return RelayConfig{
		PrimaryURL:          "",
		FallbackURLs:        []string{},
		AuthToken:           "",
		HMACKey:             nil,
		Timeout:             10 * time.Second,
		MaxFrameSize:        256 * 1024,
		ReplayWindow:        24 * time.Hour,
		HealthCheckInterval: 30 * time.Second,
		UnhealthyThreshold:  3,
		OrderedDelivery:     true,
		ReplayFrom:          0,
		BackoffBase:         1 * time.Second,
		BackoffMax:          5 * time.Minute,
		BackoffMultiplier:   2.0,
	}
}

// RelayMessage represents a message sent through the relay
type RelayMessage struct {
	ID        string            `json:"id"`
	Timestamp int64             `json:"timestamp"`
	Sequence  uint64            `json:"sequence"`
	Topic     string            `json:"topic"`
	Payload   []byte            `json:"payload"`
	Signature string            `json:"signature"`
	HMAC      string            `json:"hmac"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// RelayClient is a client for a single relay endpoint
type RelayClient struct {
	mu               sync.RWMutex
	url              string
	config           RelayConfig
	httpClient       *http.Client
	sequence         uint64
	lastHealthCheck  time.Time
	healthy          bool
	consecutiveFails int
	lastError        error
	stopChan         chan struct{}
	healthTicker     *time.Ticker
}

// NewRelayClient creates a new relay client
func NewRelayClient(url string, config RelayConfig) *RelayClient {
	rc := &RelayClient{
		url:      url,
		config:   config,
		healthy:  true,
		stopChan: make(chan struct{}),
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
	}

	rc.healthTicker = time.NewTicker(config.HealthCheckInterval)
	go rc.healthCheckLoop()

	return rc
}

// Post sends a message to the relay
func (rc *RelayClient) Post(ctx context.Context, msg *RelayMessage) error {
	rc.mu.RLock()
	if !rc.healthy {
		rc.mu.RUnlock()
		return ErrRelayUnhealthy
	}
	rc.mu.RUnlock()

	// Sign and HMAC the message
	if err := rc.signMessage(msg); err != nil {
		return fmt.Errorf("failed to sign message: %w", err)
	}

	// Marshal
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	// Check size
	if len(data) > rc.config.MaxFrameSize {
		return ErrFrameTooLarge
	}

	// Send
	req, err := http.NewRequestWithContext(ctx, "POST", rc.url, strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", "ENCRYPTED_HIVE_FRAME")

	resp, err := rc.httpClient.Do(req)
	if err != nil {
		rc.recordFailure(err)
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("relay returned %s", resp.Status)
		rc.recordFailure(err)
		return err
	}

	rc.recordSuccess()
	return nil
}

// LongPoll starts long-polling the relay
func (rc *RelayClient) LongPoll(ctx context.Context, handler func(*RelayMessage) error) error {
	url := rc.url + "/json"

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			cancel()
			return fmt.Errorf("failed to create request: %w", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			rc.recordFailure(err)
			time.Sleep(5 * time.Second)
			continue
		}

		err = rc.consumeStream(resp.Body, handler)
		cancel()

		if err != nil {
			rc.recordFailure(err)
			time.Sleep(5 * time.Second)
			continue
		}

		rc.recordSuccess()
		time.Sleep(2 * time.Second)
	}
}

func (rc *RelayClient) consumeStream(body io.Reader, handler func(*RelayMessage) error) error {
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		var relayMsg map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &relayMsg); err != nil {
			continue
		}

		event, _ := relayMsg["event"].(string)
		if event != "message" {
			continue
		}

		title, _ := relayMsg["title"].(string)
		if title != "ENCRYPTED_HIVE_FRAME" {
			continue
		}

		message, _ := relayMsg["message"].(string)
		if message == "" {
			continue
		}

		// Decrypt and verify
		msg, err := rc.verifyMessage(message)
		if err != nil {
			continue
		}

		if err := handler(msg); err != nil {
			return err
		}
	}

	return scanner.Err()
}

func (rc *RelayClient) signMessage(msg *RelayMessage) error {
	// Add sequence for ordering
	atomic.AddUint64(&msg.Sequence, 1)

	// Sign with HMAC if key available
	if len(rc.config.HMACKey) > 0 {
		mac := hmac.New(sha256.New, rc.config.HMACKey)
		data, _ := json.Marshal(msg)
		mac.Write(data)
		msg.HMAC = hex.EncodeToString(mac.Sum(nil))
	}

	// Sign with Ed25519 if key available
	// This would use the identity's signing key
	return nil
}

func (rc *RelayClient) verifyMessage(data string) (*RelayMessage, error) {
	var msg RelayMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		return nil, err
	}

	// Verify HMAC
	if len(rc.config.HMACKey) > 0 && msg.HMAC != "" {
		mac := hmac.New(sha256.New, rc.config.HMACKey)
		msgCopy := msg
		msgCopy.HMAC = ""
		data, _ := json.Marshal(msgCopy)
		mac = hmac.New(sha256.New, rc.config.HMACKey)
		mac.Write(data)
		expected := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(msg.HMAC), []byte(expected)) {
			return nil, ErrAuthenticationFailed
		}
	}

	return &msg, nil
}

func (rc *RelayClient) healthCheckLoop() {
	ticker := time.NewTicker(rc.config.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-rc.stopChan:
			return
		case <-rc.healthTicker.C:
			rc.healthCheck()
		}
	}
}

func (rc *RelayClient) healthCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", rc.url+"/json", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		rc.recordFailure(err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		rc.recordFailure(fmt.Errorf("health check returned %s", resp.Status))
		return
	}

	rc.recordSuccess()
}

func (rc *RelayClient) recordSuccess() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.healthy = true
	rc.consecutiveFails = 0
	rc.lastError = nil
}

func (rc *RelayClient) recordFailure(err error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.consecutiveFails++
	rc.lastError = err
	if rc.consecutiveFails >= rc.config.UnhealthyThreshold {
		rc.healthy = false
	}
}

func (rc *RelayClient) IsHealthy() bool {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.healthy
}

func (rc *RelayClient) GetStats() map[string]interface{} {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return map[string]interface{}{
		"url":               rc.url,
		"healthy":           rc.healthy,
		"consecutive_fails": rc.consecutiveFails,
		"last_error":        rc.lastError,
		"last_health_check": rc.lastHealthCheck,
	}
}

func (rc *RelayClient) Stop() {
	close(rc.stopChan)
	if rc.healthTicker != nil {
		rc.healthTicker.Stop()
	}
}

// MultiRelay manages multiple relay clients with failover
type MultiRelay struct {
	mu           sync.RWMutex
	config       RelayConfig
	clients      []*RelayClient
	current      int
	sequence     uint64
	pending      map[uint64]*PendingMessage
	pendingMu    sync.Mutex
	stopChan     chan struct{}
	healthTicker *time.Ticker
	failoverChan chan struct{}
}

type PendingMessage struct {
	Message   *RelayMessage
	Attempts  int
	LastTry   time.Time
	NextRetry time.Time
	Resolve   func(error)
}

// NewMultiRelay creates a new multi-relay manager
func NewMultiRelay(config RelayConfig) *MultiRelay {
	urls := []string{config.PrimaryURL}
	urls = append(urls, config.FallbackURLs...)

	mr := &MultiRelay{
		config:   config,
		clients:  make([]*RelayClient, 0, len(urls)),
		pending:  make(map[uint64]*PendingMessage),
		stopChan: make(chan struct{}),
	}

	for _, url := range urls {
		client := NewRelayClient(url, config)
		mr.clients = append(mr.clients, client)
	}

	mr.healthTicker = time.NewTicker(config.HealthCheckInterval)
	go mr.healthCheckLoop()

	return mr
}

// Post sends a message with automatic failover
func (mr *MultiRelay) Post(ctx context.Context, msg *RelayMessage) error {
	mr.mu.RLock()
	if len(mr.clients) == 0 {
		mr.mu.RUnlock()
		return ErrNoRelayAvailable
	}

	// Get current healthy client
	client := mr.getHealthyClient()
	mr.mu.RUnlock()

	if client == nil {
		return ErrNoRelayAvailable
	}

	// Set sequence for ordering
	msg.Sequence = atomic.AddUint64(&mr.sequence, 1)
	msg.Timestamp = time.Now().Unix()

	// Try current client
	err := client.Post(ctx, msg)
	if err == nil {
		return nil
	}

	// Failover to next healthy client
	mr.failover()
	return mr.Post(ctx, msg)
}

// LongPoll starts long-polling across all relays
func (mr *MultiRelay) LongPoll(ctx context.Context, handler func(*RelayMessage) error) error {
	mr.mu.RLock()
	if len(mr.clients) == 0 {
		mr.mu.RUnlock()
		return ErrNoRelayAvailable
	}

	client := mr.getHealthyClient()
	mr.mu.RUnlock()

	if client == nil {
		return ErrNoRelayAvailable
	}

	return client.LongPoll(ctx, handler)
}

func (mr *MultiRelay) getHealthyClient() *RelayClient {
	for _, client := range mr.clients {
		if client.IsHealthy() {
			return client
		}
	}
	return nil
}

func (mr *MultiRelay) failover() {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	// Find next healthy client
	for i := 0; i < len(mr.clients); i++ {
		next := (mr.current + i + 1) % len(mr.clients)
		if mr.clients[next].IsHealthy() {
			mr.current = next
			return
		}
	}
}

func (mr *MultiRelay) healthCheckLoop() {
	ticker := time.NewTicker(mr.config.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-mr.stopChan:
			return
		case <-mr.healthTicker.C:
			mr.mu.RLock()
			for _, client := range mr.clients {
				client.healthCheck()
			}
			mr.mu.RUnlock()
		}
	}
}

func (mr *MultiRelay) GetStats() map[string]interface{} {
	mr.mu.RLock()
	defer mr.mu.RUnlock()

	clients := make([]map[string]interface{}, len(mr.clients))
	for i, client := range mr.clients {
		clients[i] = client.GetStats()
		clients[i]["current"] = i == mr.current
	}

	return map[string]interface{}{
		"current": mr.current,
		"clients": clients,
		"pending": len(mr.pending),
	}
}

func (mr *MultiRelay) Stop() {
	close(mr.stopChan)
	if mr.healthTicker != nil {
		mr.healthTicker.Stop()
	}
	for _, client := range mr.clients {
		client.Stop()
	}
}

// OrderedDelivery ensures ordered delivery of messages
type OrderedDelivery struct {
	mu          sync.Mutex
	expectedSeq uint64
	buffer      map[uint64]*RelayMessage
	handler     func(*RelayMessage) error
	flushTicker *time.Ticker
	stopChan    chan struct{}
}

// NewOrderedDelivery creates a new ordered delivery handler
func NewOrderedDelivery(handler func(*RelayMessage) error) *OrderedDelivery {
	od := &OrderedDelivery{
		expectedSeq: 1,
		buffer:      make(map[uint64]*RelayMessage),
		handler:     handler,
		stopChan:    make(chan struct{}),
	}

	od.flushTicker = time.NewTicker(100 * time.Millisecond)
	go od.flushLoop()

	return od
}

func (od *OrderedDelivery) Deliver(msg *RelayMessage) error {
	od.mu.Lock()
	defer od.mu.Unlock()

	if msg.Sequence < od.expectedSeq {
		return ErrDuplicateFrame
	}

	if msg.Sequence == od.expectedSeq {
		// Deliver immediately
		if err := od.handler(msg); err != nil {
			return err
		}
		od.expectedSeq++
		od.flushBuffer()
		return nil
	}

	// Buffer for later
	od.buffer[msg.Sequence] = msg
	return nil
}

func (od *OrderedDelivery) flushBuffer() {
	for {
		msg, ok := od.buffer[od.expectedSeq]
		if !ok {
			break
		}
		delete(od.buffer, od.expectedSeq)
		if err := od.handler(msg); err != nil {
			// Log error but continue
		}
		od.expectedSeq++
	}
}

func (od *OrderedDelivery) flushLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-od.stopChan:
			return
		case <-od.flushTicker.C:
			od.mu.Lock()
			od.flushBuffer()
			od.mu.Unlock()
		}
	}
}

func (od *OrderedDelivery) Stop() {
	close(od.stopChan)
	if od.flushTicker != nil {
		od.flushTicker.Stop()
	}
}

func (od *OrderedDelivery) GetStats() map[string]interface{} {
	od.mu.Lock()
	defer od.mu.Unlock()
	return map[string]interface{}{
		"expected_sequence": od.expectedSeq,
		"buffered":          len(od.buffer),
	}
}
