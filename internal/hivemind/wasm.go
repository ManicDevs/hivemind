package hivemind

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

var (
	ErrModuleNotFound   = fmt.Errorf("module not found")
	ErrFunctionNotFound = fmt.Errorf("function not found")
	ErrFuelExhausted    = fmt.Errorf("fuel exhausted")
	ErrMemoryLimit      = fmt.Errorf("memory limit exceeded")
)

type WASMConfig struct {
	MaxFuel         uint64
	MemoryLimit     uint32 // pages (64KB each)
	EnableWASI      bool
	AllowNetwork    bool
	AllowFileSystem bool
	ModuleCacheSize int
}

func DefaultWASMConfig() WASMConfig {
	return WASMConfig{
		MaxFuel:         10_000_000,
		MemoryLimit:     256, // 16MB
		EnableWASI:      false,
		AllowNetwork:    false,
		AllowFileSystem: false,
		ModuleCacheSize: 50,
	}
}

type compiledModule struct {
	module   api.Module
	compiled wazero.CompiledModule
}

type WASMEngine struct {
	mu           sync.RWMutex
	config       WASMConfig
	runtime      wazero.Runtime
	modules      map[string]*compiledModule
	moduleHashes map[string]string
	instances    map[string]api.Module
	fuelTracker  map[string]uint64
	cacheOrder   []string
	stopChan     chan struct{}
}

func NewWASMEngine(config WASMConfig) (*WASMEngine, error) {
	we := &WASMEngine{
		config:       config,
		runtime:      wazero.NewRuntime(context.Background()),
		modules:      make(map[string]*compiledModule),
		moduleHashes: make(map[string]string),
		instances:    make(map[string]api.Module),
		fuelTracker:  make(map[string]uint64),
		cacheOrder:   make([]string, 0),
		stopChan:     make(chan struct{}),
	}
	return we, nil
}

func (we *WASMEngine) LoadModule(ctx context.Context, name, path string) error {
	we.mu.Lock()
	defer we.mu.Unlock()

	if _, ok := we.modules[name]; ok {
		return fmt.Errorf("module %s already loaded", name)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read module: %w", err)
	}
	compiled, err := we.runtime.CompileModule(ctx, data)
	if err != nil {
		return fmt.Errorf("failed to compile module: %w", err)
	}

	we.modules[name] = &compiledModule{
		module:   nil, // will be set on instantiate
		compiled: compiled,
	}
	we.moduleHashes[name] = hashModule(data)
	we.cacheOrder = append(we.cacheOrder, name)

	// Evict if over cache size
	if len(we.cacheOrder) > we.config.ModuleCacheSize {
		oldest := we.cacheOrder[0]
		we.cacheOrder = we.cacheOrder[1:]
		delete(we.modules, oldest)
		delete(we.moduleHashes, oldest)
		delete(we.instances, oldest)
	}

	return nil
}

func (we *WASMEngine) InstantiateModule(ctx context.Context, name string) (api.Module, error) {
	we.mu.Lock()
	defer we.mu.Unlock()

	cm, ok := we.modules[name]
	if !ok {
		return nil, ErrModuleNotFound
	}

	instance, err := we.runtime.InstantiateModule(ctx, cm.compiled, wazero.NewModuleConfig().WithName(name))
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate: %w", err)
	}

	we.instances[name] = instance
	we.fuelTracker[name] = we.config.MaxFuel

	return instance, nil
}

func (we *WASMEngine) CallFunction(ctx context.Context, module, function string, args ...interface{}) (interface{}, error) {
	we.mu.RLock()
	instance, ok := we.instances[module]
	we.mu.RUnlock()

	if !ok {
		return nil, ErrModuleNotFound
	}

	export := instance.ExportedFunction(function)
	if export == nil {
		return nil, ErrFunctionNotFound
	}

	// Check fuel
	we.mu.Lock()
	fuel := we.fuelTracker[module]
	if fuel == 0 {
		we.mu.Unlock()
		return nil, ErrFuelExhausted
	}
	we.fuelTracker[module] = fuel - 1
	we.mu.Unlock()

	// Call with timeout
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	// Convert args to uint64
	vals := make([]uint64, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case int32:
			vals[i] = uint64(v)
		case int64:
			vals[i] = uint64(v)
		case uint32:
			vals[i] = uint64(v)
		case uint64:
			vals[i] = v
		case float32:
			vals[i] = uint64(api.EncodeF32(v))
		case float64:
			vals[i] = uint64(api.EncodeF64(v))
		default:
			return nil, fmt.Errorf("unsupported arg type: %T", arg)
		}
	}

	uint64Results, err := export.Call(ctx, vals...)
	if err != nil {
		return nil, err
	}

	// Convert results back
	results := make([]interface{}, len(uint64Results))
	for i, r := range uint64Results {
		results[i] = r
	}

	if len(results) == 1 {
		return results[0], nil
	}
	return results, nil
}

func (we *WASMEngine) GetModule(name string) (api.Module, bool) {
	we.mu.RLock()
	defer we.mu.RUnlock()
	_, ok := we.modules[name]
	return nil, ok
}

func (we *WASMEngine) GetInstance(name string) (api.Module, bool) {
	we.mu.RLock()
	defer we.mu.RUnlock()
	i, ok := we.instances[name]
	return i, ok
}

func (we *WASMEngine) GetFuel(module string) uint64 {
	we.mu.RLock()
	defer we.mu.RUnlock()
	return we.fuelTracker[module]
}

func (we *WASMEngine) Refuel(module string, fuel uint64) {
	we.mu.Lock()
	defer we.mu.Unlock()
	we.fuelTracker[module] = fuel
}

func (we *WASMEngine) UnloadModule(name string) error {
	we.mu.Lock()
	defer we.mu.Unlock()

	delete(we.modules, name)
	delete(we.moduleHashes, name)
	delete(we.instances, name)
	delete(we.fuelTracker, name)

	for i, n := range we.cacheOrder {
		if n == name {
			we.cacheOrder = append(we.cacheOrder[:i], we.cacheOrder[i+1:]...)
			break
		}
	}
	return nil
}

func (we *WASMEngine) ListModules() []string {
	we.mu.RLock()
	defer we.mu.RUnlock()

	modules := make([]string, 0, len(we.modules))
	for name := range we.modules {
		modules = append(modules, name)
	}
	return modules
}

func (we *WASMEngine) Stop() {
	close(we.stopChan)
	if we.runtime != nil {
		we.runtime.Close(context.Background())
	}
}

type MindImports struct {
	mind *Mind
}

func NewMindImports(mind *Mind) *MindImports {
	return &MindImports{mind: mind}
}

func (mi *MindImports) GetImports() map[string]interface{} {
	return map[string]interface{}{
		"sense":     mi.sense,
		"act":       mi.act,
		"remember":  mi.remember,
		"broadcast": mi.broadcast,
		"sleep_ms":  sleepMS,
	}
}

func (mi *MindImports) sense(args ...interface{}) (interface{}, error) {
	model := mi.mind.Observe()
	return model, nil
}

func (mi *MindImports) act(args ...interface{}) (interface{}, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("act requires goal name")
	}
	goalName := args[0].(string)
	return fmt.Sprintf("acted on %s", goalName), nil
}

func (mi *MindImports) remember(args ...interface{}) (interface{}, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("remember requires data")
	}
	return "remembered", nil
}

func (mi *MindImports) broadcast(args ...interface{}) (interface{}, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("broadcast requires payload")
	}
	return "broadcasted", nil
}

func sleepMS(args ...interface{}) (interface{}, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("sleep_ms requires milliseconds")
	}
	ms := args[0].(int32)
	time.Sleep(time.Duration(ms) * time.Millisecond)
	return "slept", nil
}

func hashModule(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:16])
}
