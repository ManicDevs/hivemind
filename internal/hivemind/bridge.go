package hivemind

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

var (
	ErrBridgeNotFound   = errors.New("bridge not found")
	ErrBridgeClosed     = errors.New("bridge closed")
	ErrMeshNotConnected = errors.New("mesh not connected")
)

type BridgeConfig struct {
	LocalMeshName     string
	RemoteMeshName    string
	RemoteAddress     string
	Capabilities      []Capability
	AutoReconnect     bool
	ReconnectInterval time.Duration
	MaxMessageSize    int
	HeartbeatInterval time.Duration
	HandshakeTimeout  time.Duration
}

func DefaultBridgeConfig() BridgeConfig {
	return BridgeConfig{
		Capabilities:      []Capability{CapMeshJoin, CapFrameSend, CapFrameReceive},
		AutoReconnect:     true,
		ReconnectInterval: 30 * time.Second,
		MaxMessageSize:    256 * 1024,
		HeartbeatInterval: 10 * time.Second,
		HandshakeTimeout:  10 * time.Second,
	}
}

type Bridge struct {
	config          BridgeConfig
	localMesh       *Swarm
	remoteConn      net.Conn
	remotePubKey    ed25519.PublicKey
	localPrivKey    ed25519.PrivateKey
	localPubKey     ed25519.PublicKey
	connected       bool
	stopChan        chan struct{}
	mu              sync.RWMutex
	pendingCalls    map[string]chan *BridgeResponse
	callID          uint64
	heartbeatTicker *time.Ticker
	reconnectTicker *time.Ticker
}

type BridgeMessage struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *BridgeError    `json:"error,omitempty"`
	Timestamp int64           `json:"timestamp"`
	Signature string          `json:"signature"`
}

type BridgeError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (e *BridgeError) Error() string {
	return fmt.Sprintf("bridge error %d: %s", e.Code, e.Message)
}

type BridgeResponse struct {
	Result json.RawMessage
	Error  *BridgeError
	Err    error
}

func NewBridge(config BridgeConfig, localMesh *Swarm) (*Bridge, error) {
	_, priv := newIdentity()

	b := &Bridge{
		config:       config,
		localMesh:    localMesh,
		localPrivKey: priv,
		localPubKey:  priv.Public().(ed25519.PublicKey),
		connected:    false,
		pendingCalls: make(map[string]chan *BridgeResponse),
		stopChan:     make(chan struct{}),
	}

	return b, nil
}

func (b *Bridge) Connect(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.connected {
		return nil
	}

	conn, err := net.DialTimeout("tcp", b.config.RemoteAddress, b.config.HandshakeTimeout)
	if err != nil {
		return fmt.Errorf("failed to connect to remote mesh: %w", err)
	}

	b.remoteConn = conn

	if err := b.handshake(ctx); err != nil {
		conn.Close()
		return fmt.Errorf("handshake failed: %w", err)
	}

	b.connected = true
	b.stopChan = make(chan struct{})

	go b.readLoop()
	go b.heartbeatLoop()

	if b.config.AutoReconnect {
		b.reconnectTicker = time.NewTicker(b.config.ReconnectInterval)
		go b.reconnectLoop()
	}

	return nil
}

func (b *Bridge) handshake(ctx context.Context) error {
	handshake := map[string]interface{}{
		"type":         "handshake",
		"mesh_name":    b.config.LocalMeshName,
		"pub_key":      b.localPubKey,
		"capabilities": b.config.Capabilities,
		"timestamp":    time.Now().Unix(),
	}

	data, _ := json.Marshal(handshake)
	signed, err := b.sign(data)
	if err != nil {
		return err
	}

	msg := BridgeMessage{
		ID:        generateMessageID(),
		Type:      "handshake",
		Params:    data,
		Signature: signed,
		Timestamp: time.Now().Unix(),
	}

	if err := b.send(msg); err != nil {
		return err
	}

	callID := msg.ID
	b.mu.Lock()
	respChan := make(chan *BridgeResponse, 1)
	b.pendingCalls[callID] = respChan
	b.mu.Unlock()

	select {
	case resp := <-respChan:
		if resp.Error != nil {
			return fmt.Errorf("handshake failed: %s", resp.Error.Message)
		}
		var respData map[string]interface{}
		json.Unmarshal(resp.Result, &respData)
		if pk, ok := respData["pub_key"].(string); ok {
			pubBytes, _ := hex.DecodeString(pk)
			b.remotePubKey = ed25519.PublicKey(pubBytes)
		}
		return nil
	case <-time.After(b.config.HandshakeTimeout):
		return errors.New("handshake timeout")
	}
}

func (b *Bridge) Call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	b.mu.RLock()
	if !b.connected {
		b.mu.RUnlock()
		return nil, ErrMeshNotConnected
	}
	b.mu.RUnlock()

	callID := generateCallID()
	respChan := make(chan *BridgeResponse, 1)

	b.mu.Lock()
	b.pendingCalls[callID] = respChan
	b.mu.Unlock()

	paramsData, _ := json.Marshal(params)
	msg := BridgeMessage{
		ID:        callID,
		Type:      "request",
		Method:    method,
		Params:    paramsData,
		Timestamp: time.Now().Unix(),
	}

	if err := b.send(msg); err != nil {
		return nil, err
	}

	select {
	case resp := <-respChan:
		return resp.Result, resp.Error
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, errors.New("call timeout")
	}
}

func (b *Bridge) Notify(method string, params interface{}) error {
	b.mu.RLock()
	if !b.connected {
		b.mu.RUnlock()
		return ErrMeshNotConnected
	}
	b.mu.RUnlock()

	paramsData, _ := json.Marshal(params)
	msg := BridgeMessage{
		ID:        generateMessageID(),
		Type:      "event",
		Method:    method,
		Params:    paramsData,
		Timestamp: time.Now().Unix(),
	}

	return b.send(msg)
}

func (b *Bridge) send(msg BridgeMessage) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.connected || b.remoteConn == nil {
		return ErrMeshNotConnected
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	signed, err := b.sign(data)
	if err != nil {
		return err
	}

	msg.Signature = signed
	data, _ = json.Marshal(msg)

	_, err = b.remoteConn.Write(append(data, '\n'))
	return err
}

func (b *Bridge) sign(data []byte) (string, error) {
	sig := ed25519.Sign(b.localPrivKey, data)
	return hex.EncodeToString(sig), nil
}

func (b *Bridge) verify(data []byte, signature string, pubKey ed25519.PublicKey) bool {
	sig, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(pubKey, data, sig)
}

func (b *Bridge) readLoop() {
	scanner := bufio.NewScanner(b.remoteConn)
	for scanner.Scan() {
		select {
		case <-b.stopChan:
			return
		default:
		}

		var msg BridgeMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}

		data, _ := json.Marshal(BridgeMessage{
			ID:        msg.ID,
			Type:      msg.Type,
			Method:    msg.Method,
			Params:    msg.Params,
			Result:    msg.Result,
			Error:     msg.Error,
			Timestamp: msg.Timestamp,
		})
		if !b.verify(data, msg.Signature, b.remotePubKey) {
			continue
		}

		b.handleMessage(&msg)
	}
}

func (b *Bridge) handleMessage(msg *BridgeMessage) {
	switch msg.Type {
	case "response":
		b.mu.Lock()
		if ch, ok := b.pendingCalls[msg.ID]; ok {
			resp := &BridgeResponse{
				Result: msg.Result,
				Error:  msg.Error,
			}
			if msg.Error != nil {
				resp.Err = fmt.Errorf("remote error: %s", msg.Error.Message)
			}
			ch <- resp
			delete(b.pendingCalls, msg.ID)
		}
		b.mu.Unlock()

	case "event":
		// Handle event notification
	case "heartbeat":
		b.send(BridgeMessage{
			ID:        generateMessageID(),
			Type:      "heartbeat",
			Timestamp: time.Now().Unix(),
		})
	}
}

func (b *Bridge) heartbeatLoop() {
	b.heartbeatTicker = time.NewTicker(b.config.HeartbeatInterval)
	defer b.heartbeatTicker.Stop()

	for {
		select {
		case <-b.stopChan:
			return
		case <-b.heartbeatTicker.C:
			b.send(BridgeMessage{
				ID:        generateMessageID(),
				Type:      "heartbeat",
				Timestamp: time.Now().Unix(),
			})
		}
	}
}

func (b *Bridge) reconnectLoop() {
	for {
		select {
		case <-b.stopChan:
			return
		case <-b.reconnectTicker.C:
			if !b.connected && b.config.AutoReconnect {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				b.Connect(ctx)
				cancel()
			}
		}
	}
}

func (b *Bridge) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.connected {
		return nil
	}

	b.connected = false
	close(b.stopChan)

	if b.heartbeatTicker != nil {
		b.heartbeatTicker.Stop()
	}
	if b.reconnectTicker != nil {
		b.reconnectTicker.Stop()
	}

	if b.remoteConn != nil {
		b.remoteConn.Close()
	}

	for _, ch := range b.pendingCalls {
		close(ch)
	}

	return nil
}

func (b *Bridge) IsConnected() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.connected
}

func (b *Bridge) GetRemotePubKey() ed25519.PublicKey {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.remotePubKey
}

func (b *Bridge) GetLocalPubKey() ed25519.PublicKey {
	return b.localPubKey
}

type BridgeManager struct {
	mu       sync.RWMutex
	config   BridgeConfig
	bridges  map[string]*Bridge
	mesh     *Swarm
	stopChan chan struct{}
}

func NewBridgeManager(config BridgeConfig, mesh *Swarm) *BridgeManager {
	return &BridgeManager{
		config:   config,
		mesh:     mesh,
		bridges:  make(map[string]*Bridge),
		stopChan: make(chan struct{}),
	}
}

func (bm *BridgeManager) CreateBridge(ctx context.Context, name string, config BridgeConfig) (*Bridge, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if _, ok := bm.bridges[name]; ok {
		return nil, fmt.Errorf("bridge %s already exists", name)
	}

	config.LocalMeshName = bm.config.LocalMeshName
	bridge, err := NewBridge(config, bm.mesh)
	if err != nil {
		return nil, err
	}

	if err := bridge.Connect(ctx); err != nil {
		return nil, err
	}

	bm.bridges[name] = bridge
	return bridge, nil
}

func (bm *BridgeManager) GetBridge(name string) (*Bridge, bool) {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	bridge, ok := bm.bridges[name]
	return bridge, ok
}

func (bm *BridgeManager) RemoveBridge(name string) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	bridge, ok := bm.bridges[name]
	if !ok {
		return ErrBridgeNotFound
	}

	bridge.Close()
	delete(bm.bridges, name)
	return nil
}

func (bm *BridgeManager) ListBridges() []string {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	names := make([]string, 0, len(bm.bridges))
	for name := range bm.bridges {
		names = append(names, name)
	}
	return names
}

func (bm *BridgeManager) GetStats() map[string]interface{} {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	stats := make(map[string]interface{})
	for name, bridge := range bm.bridges {
		stats[name] = map[string]interface{}{
			"connected": bridge.IsConnected(),
			"remote":    bridge.config.RemoteAddress,
		}
	}
	return stats
}

func (bm *BridgeManager) Stop() {
	close(bm.stopChan)
	bm.mu.Lock()
	for _, bridge := range bm.bridges {
		bridge.Close()
	}
	bm.bridges = nil
	bm.mu.Unlock()
}

func generateMessageID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func generateCallID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
