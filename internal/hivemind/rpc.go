package hivemind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrMethodNotFound     = errors.New("method not found")
	ErrInvalidParams      = errors.New("invalid parameters")
	ErrTimeout            = errors.New("request timeout")
	ErrUnauthorized       = errors.New("unauthorized")
	ErrServiceUnavailable = errors.New("service unavailable")
)

type RPCConfig struct {
	DefaultTimeout    time.Duration
	MaxConcurrentCalls int
	EnableCompression  bool
	CompressionLevel   int
	EnableMetrics      bool
	EnableTracing      bool
}

func DefaultRPCConfig() RPCConfig {
	return RPCConfig{
		DefaultTimeout:    30 * time.Second,
		MaxConcurrentCalls: 100,
		EnableCompression:  false,
		CompressionLevel:   3,
		EnableMetrics:      true,
		EnableTracing:      true,
	}
}

type RPCRequest struct {
	ID        string          `json:"id"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
	Timeout   int64           `json:"timeout,omitempty"`
	Token     string          `json:"token,omitempty"`
	TraceID   string          `json:"trace_id,omitempty"`
	SpanID    string          `json:"span_id,omitempty"`
}

type RPCResponse struct {
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type MethodHandler func(ctx context.Context, params json.RawMessage) (interface{}, error)

type RPCServer struct {
	mu           sync.RWMutex
	config       RPCConfig
	methods      map[string]MethodHandler
	middleware   []Middleware
	capManager   CapabilityManager
	stopChan     chan struct{}
	activeCalls  int64
	totalCalls   int64
	totalErrors  int64
}

type Middleware func(next MethodHandler) MethodHandler

func NewRPCServer(config RPCConfig, capManager CapabilityManager) *RPCServer {
	return &RPCServer{
		config:     config,
		methods:    make(map[string]MethodHandler),
		capManager: capManager,
		stopChan:   make(chan struct{}),
	}
}

func (s *RPCServer) RegisterMethod(name string, handler MethodHandler, requiredCaps ...Capability) {
	s.mu.Lock()
	defer s.mu.Unlock()

	wrapped := func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if len(requiredCaps) > 0 {
			tokenStr := getTokenFromContext(ctx)
			if tokenStr == "" {
				return nil, ErrUnauthorized
			}

token, err := s.capManager.VerifyToken(ctx, tokenStr, "")
		if err != nil {
			return nil, ErrUnauthorized
		}

		for _, cap := range requiredCaps {
			if !s.capManager.HasCapability(token, cap) {
				return nil, ErrInsufficientScope
			}
		}
		}
		return handler(ctx, params)
	}

	s.methods[name] = wrapped
}

func (s *RPCServer) Use(middleware Middleware) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.middleware = append(s.middleware, middleware)
}

func (s *RPCServer) HandleRequest(ctx context.Context, req *RPCRequest) *RPCResponse {
	if atomic.LoadInt64(&s.activeCalls) >= int64(s.config.MaxConcurrentCalls) {
		return &RPCResponse{
			ID: req.ID,
			Error: &RPCError{
				Code:    -32000,
				Message: ErrServiceUnavailable.Error(),
			},
		}
	}

	atomic.AddInt64(&s.activeCalls, 1)
	defer atomic.AddInt64(&s.activeCalls, -1)
	atomic.AddInt64(&s.totalCalls, 1)

	baseCtx := context.Background()
	var _ context.CancelFunc
	if req.Timeout > 0 {
		ctx, _ = context.WithTimeout(baseCtx, time.Duration(req.Timeout)*time.Millisecond)
	} else {
		ctx, _ = context.WithTimeout(baseCtx, s.config.DefaultTimeout)
	}

	if req.Token != "" {
		ctx = context.WithValue(ctx, "token", req.Token)
	}

	if req.TraceID != "" {
		ctx = context.WithValue(ctx, "trace_id", req.TraceID)
	}
	if req.SpanID != "" {
		ctx = context.WithValue(ctx, "span_id", req.SpanID)
	}

	s.mu.RLock()
	middleware := make([]Middleware, len(s.middleware))
	copy(middleware, s.middleware)
	s.mu.RUnlock()

	var handler MethodHandler
	s.mu.RLock()
	handler, ok := s.methods[req.Method]
	s.mu.RUnlock()

	if !ok {
		return &RPCResponse{
			ID: req.ID,
			Error: &RPCError{
				Code:    -32601,
				Message: ErrMethodNotFound.Error(),
			},
		}
	}

	for i := len(middleware) - 1; i >= 0; i-- {
		next := handler
		handler = middleware[i](next)
	}

	result, err := handler(ctx, req.Params)

	if s.config.EnableMetrics {
		// Record metrics
	}

	if err != nil {
		atomic.AddInt64(&s.totalErrors, 1)
		rpcErr := &RPCError{
			Code:    -32000,
			Message: err.Error(),
		}
		if errors.Is(err, ErrUnauthorized) {
			rpcErr.Code = -32001
		} else if errors.Is(err, context.DeadlineExceeded) {
			rpcErr.Code = -32002
		} else if errors.Is(err, ErrInvalidParams) {
			rpcErr.Code = -32602
		}
		return &RPCResponse{
			ID:    req.ID,
			Error: rpcErr,
		}
	}

	resultData, err := json.Marshal(result)
	if err != nil {
		return &RPCResponse{
			ID: req.ID,
			Error: &RPCError{
				Code:    -32603,
				Message: "internal error: failed to marshal result",
			},
		}
	}

	return &RPCResponse{
		ID:     req.ID,
		Result: resultData,
	}
}

func (s *RPCServer) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"active_calls":  atomic.LoadInt64(&s.activeCalls),
		"total_calls":   atomic.LoadInt64(&s.totalCalls),
		"total_errors":  atomic.LoadInt64(&s.totalErrors),
		"methods_count": len(s.methods),
	}
}

func (s *RPCServer) Stop() {
	close(s.stopChan)
}

type RPCClient struct {
	config      RPCConfig
	transport   Transport
	callID      uint64
	pending     map[string]chan *RPCResponse
	mu          sync.RWMutex
	stopChan    chan struct{}
}

type Transport interface {
	Send(ctx context.Context, req *RPCRequest) (*RPCResponse, error)
	Close() error
}

func NewRPCClient(transport Transport, config RPCConfig) *RPCClient {
	return &RPCClient{
		config:    config,
		transport: transport,
		pending:   make(map[string]chan *RPCResponse),
		stopChan:  make(chan struct{}),
	}
}

func (c *RPCClient) Call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	callID := atomic.AddUint64(&c.callID, 1)
	id := fmt.Sprintf("call-%d", callID)

	req := &RPCRequest{
		ID:     fmt.Sprintf("%d", callID),
		Method: method,
	}

	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		req.Params = data
	}

	req.Timeout = int64(c.config.DefaultTimeout.Milliseconds())

	respChan := make(chan *RPCResponse, 1)
	c.mu.Lock()
	c.pending[id] = respChan
	c.mu.Unlock()

	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	return resp.Result, nil
}

func (c *RPCClient) Close() error {
	close(c.stopChan)
	return nil
}

type MethodRegistry struct {
	mu       sync.RWMutex
	methods  map[string]MethodInfo
}

type MethodInfo struct {
	Name        string
	Handler     MethodHandler
	RequiredCaps []Capability
	Description string
	ParamsSchema interface{}
	ResultSchema interface{}
}

func NewMethodRegistry() *MethodRegistry {
	return &MethodRegistry{
		methods: make(map[string]MethodInfo),
	}
}

func (mr *MethodRegistry) Register(info MethodInfo) {
	mr.mu.Lock()
	defer mr.mu.Unlock()
	mr.methods[info.Name] = info
}

func (mr *MethodRegistry) Get(name string) (MethodInfo, bool) {
	mr.mu.RLock()
	defer mr.mu.RUnlock()
	info, ok := mr.methods[name]
	return info, ok
}

func (mr *MethodRegistry) List() []MethodInfo {
	mr.mu.RLock()
	defer mr.mu.RUnlock()

	list := make([]MethodInfo, 0, len(mr.methods))
	for _, info := range mr.methods {
		list = append(list, info)
	}
	return list
}

func LoggingMiddleware(next MethodHandler) MethodHandler {
	return func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		start := time.Now()
		result, err := next(ctx, params)
		fmt.Printf("RPC call took %v, err: %v\n", time.Since(start), err)
		return result, err
	}
}

func RecoveryMiddleware(next MethodHandler) MethodHandler {
	return func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("RPC panic recovered: %v\n", r)
			}
		}()
		return next(ctx, params)
	}
}

func TimeoutMiddleware(timeout time.Duration) Middleware {
	return func(next MethodHandler) MethodHandler {
		return func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			type result struct {
				val interface{}
				err error
			}
			ch := make(chan result, 1)
			go func() {
				val, err := next(ctx, params)
				ch <- result{val, err}
			}()
			select {
			case res := <-ch:
				return res.val, res.err
			case <-ctx.Done():
				return nil, ErrTimeout
			}
		}
	}
}

func getTokenFromContext(ctx context.Context) string {
	if token, ok := ctx.Value("token").(string); ok {
		return token
	}
	return ""
}