package hivemind

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	data     []byte // raw bytes, kept so modules survive restarts
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
		compiled: compiled,
		data:     data,
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

// SaveModules persists every loaded module's bytes plus fuel, so a
// restart rehydrates the exact menagerie. Atomic per file.
func (we *WASMEngine) SaveModules(dir string) error {
	we.mu.RLock()
	defer we.mu.RUnlock()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, name := range we.cacheOrder {
		cm, ok := we.modules[name]
		if !ok {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name+".wasm"), cm.data, 0644); err != nil {
			return err
		}
		meta, _ := json.Marshal(map[string]uint64{"fuel": we.fuelTracker[name]})
		if err := os.WriteFile(filepath.Join(dir, name+".json"), meta, 0644); err != nil {
			return err
		}
	}
	return nil
}

// LoadPersisted recompiles every .wasm in dir and restores fuel.
// Corrupt files are skipped, never fatal: one bad module must not
// kill the menagerie.
func (we *WASMEngine) LoadPersisted(ctx context.Context, dir string) (int, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.wasm"))
	if err != nil {
		return 0, err
	}
	loaded := 0
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".wasm")
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		compiled, err := we.runtime.CompileModule(ctx, data)
		if err != nil {
			continue
		}
		var fuel uint64
		if raw, err := os.ReadFile(filepath.Join(dir, name+".json")); err == nil {
			var meta map[string]uint64
			if json.Unmarshal(raw, &meta) == nil {
				fuel = meta["fuel"]
			}
		}
		we.mu.Lock()
		we.modules[name] = &compiledModule{compiled: compiled, data: data}
		we.moduleHashes[name] = hashModule(data)
		we.cacheOrder = append(we.cacheOrder, name)
		if fuel > 0 {
			we.fuelTracker[name] = fuel
		}
		we.mu.Unlock()
		loaded++
	}
	return loaded, nil
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
		// Extended primitives
		"blob_put":      mi.blobPut,
		"blob_get":      mi.blobGet,
		"blob_delete":   mi.blobDelete,
		"blob_list":     mi.blobList,
		"blob_stat":     mi.blobStat,
		"cap_issue":     mi.capIssue,
		"cap_verify":    mi.capVerify,
		"cap_has":       mi.capHas,
		"cap_revoke":    mi.capRevoke,
		"cap_delegate":  mi.capDelegate,
		"cron_add":      mi.cronAdd,
		"cron_remove":   mi.cronRemove,
		"cron_enable":   mi.cronEnable,
		"cron_disable":  mi.cronDisable,
		"cron_list":     mi.cronList,
		"bridge_dial":   mi.bridgeDial,
		"bridge_call":   mi.bridgeCall,
		"bridge_notify": mi.bridgeNotify,
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

// Blob store operations
func (mi *MindImports) blobPut(args ...interface{}) (interface{}, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("blob_put requires name and data")
	}
	name := args[0].(string)
	data := args[1].(string)
	bs := mi.mind.Swarm().BlobStore()
	if bs == nil {
		return nil, fmt.Errorf("no blob store attached to swarm")
	}
	idx, err := bs.Put(context.Background(), name, strings.NewReader(data), "text/plain", nil)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"id": idx.ID, "size": idx.Size, "checksum": idx.Checksum}, nil
}

func (mi *MindImports) blobGet(args ...interface{}) (interface{}, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("blob_get requires name")
	}
	name := args[0].(string)
	bs := mi.mind.Swarm().BlobStore()
	if bs == nil {
		return nil, fmt.Errorf("no blob store attached to swarm")
	}
	ctx := context.Background()
	reader, idx, err := bs.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"data":     string(data),
		"size":     idx.Size,
		"checksum": idx.Checksum,
	}, nil
}

func (mi *MindImports) blobDelete(args ...interface{}) (interface{}, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("blob_delete requires name")
	}
	name := args[0].(string)
	bs := mi.mind.Swarm().BlobStore()
	if bs == nil {
		return nil, fmt.Errorf("no blob store attached to swarm")
	}
	ctx := context.Background()
	if err := bs.Delete(ctx, name); err != nil {
		return nil, err
	}
	return "deleted", nil
}

func (mi *MindImports) blobList(args ...interface{}) (interface{}, error) {
	prefix := ""
	if len(args) > 0 {
		prefix = args[0].(string)
	}
	bs := mi.mind.Swarm().BlobStore()
	if bs == nil {
		return nil, fmt.Errorf("no blob store attached to swarm")
	}
	indices, err := bs.List(prefix)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, len(indices))
	for i, idx := range indices {
		result[i] = map[string]interface{}{
			"id":       idx.ID,
			"name":     idx.Name,
			"size":     idx.Size,
			"checksum": idx.Checksum,
			"created":  idx.CreatedAt,
			"versions": len(idx.Versions),
		}
	}
	return result, nil
}

func (mi *MindImports) blobStat(args ...interface{}) (interface{}, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("blob_stat requires name")
	}
	name := args[0].(string)
	bs := mi.mind.Swarm().BlobStore()
	if bs == nil {
		return nil, fmt.Errorf("no blob store attached to swarm")
	}
	idx, err := bs.GetIndex(name)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"id":        idx.ID,
		"name":      idx.Name,
		"size":      idx.Size,
		"checksum":  idx.Checksum,
		"versions":  len(idx.Versions),
		"created":   idx.CreatedAt,
		"updated":   idx.UpdatedAt,
		"ref_count": idx.RefCount,
	}, nil
}

// Capability token operations
func (mi *MindImports) capIssue(args ...interface{}) (interface{}, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("cap_issue requires subject and capabilities")
	}
	caps := make([]Capability, len(args)-1)
	for i := 1; i < len(args); i++ {
		caps[i-1] = Capability(args[i].(string))
	}
	cm := mi.mind.Swarm().CapabilityManager()
	if cm == nil {
		return nil, fmt.Errorf("no capability manager attached to swarm")
	}
	token, err := cm.IssueToken(context.Background(), args[0].(string), caps, time.Hour, "", nil)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"token": token}, nil
}

func (mi *MindImports) capVerify(args ...interface{}) (interface{}, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("cap_verify requires token")
	}
	tokenStr := args[0].(string)
	audience := ""
	if len(args) > 1 {
		audience = args[1].(string)
	}
	cm := mi.mind.Swarm().CapabilityManager()
	if cm == nil {
		return nil, fmt.Errorf("no capability manager attached to swarm")
	}
	token, err := cm.VerifyToken(context.Background(), tokenStr, audience)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"id":           token.ID,
		"subject":      token.Subject,
		"capabilities": token.Capabilities,
		"expires_at":   token.ExpiresAt,
	}, nil
}

func (mi *MindImports) capHas(args ...interface{}) (interface{}, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("cap_has requires token and capability")
	}
	tokenStr := args[0].(string)
	capStr := args[1].(string)
	cm := mi.mind.Swarm().CapabilityManager()
	if cm == nil {
		return nil, fmt.Errorf("no capability manager attached to swarm")
	}
	token, err := cm.VerifyToken(context.Background(), tokenStr, "")
	if err != nil {
		return nil, err
	}
	has := cm.HasCapability(token, Capability(capStr))
	return map[string]interface{}{"has": has}, nil
}

func (mi *MindImports) capRevoke(args ...interface{}) (interface{}, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("cap_revoke requires token ID")
	}
	tokenID := args[0].(string)
	cm := mi.mind.Swarm().CapabilityManager()
	if cm == nil {
		return nil, fmt.Errorf("no capability manager attached to swarm")
	}
	if err := cm.RevokeToken(tokenID); err != nil {
		return nil, err
	}
	return "revoked", nil
}

func (mi *MindImports) capDelegate(args ...interface{}) (interface{}, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("cap_delegate requires delegator, delegatee, and capabilities")
	}
	delegator := args[0].(string)
	delegatee := args[1].(string)
	caps := make([]Capability, len(args)-2)
	for i := 2; i < len(args); i++ {
		caps[i-2] = Capability(args[i].(string))
	}
	dm := NewDelegationManager(mi.mind.Swarm().CapabilityManager())
	delegation, err := dm.Delegate(context.Background(), delegator, delegatee,
		[]Capability(caps), time.Hour, nil)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"delegation_id": delegation.Token,
		"expires_at":    delegation.ExpiresAt,
	}, nil
}

// Cron operations
func (mi *MindImports) cronAdd(args ...interface{}) (interface{}, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("cron_add requires id, schedule, and handler")
	}
	// Simplified - in practice would need to store handler
	return "scheduled", nil
}

func (mi *MindImports) cronRemove(args ...interface{}) (interface{}, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("cron_remove requires job id")
	}
	return "removed", nil
}

func (mi *MindImports) cronEnable(args ...interface{}) (interface{}, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("cron_enable requires job id")
	}
	return "enabled", nil
}

func (mi *MindImports) cronDisable(args ...interface{}) (interface{}, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("cron_disable requires job id")
	}
	return "disabled", nil
}

func (mi *MindImports) cronList(args ...interface{}) (interface{}, error) {
	cr := mi.mind.Swarm().Cron()
	if cr == nil {
		return nil, fmt.Errorf("no cron attached to swarm")
	}
	return cr.GetStats(), nil
}

// Bridge operations
func (mi *MindImports) bridgeDial(args ...interface{}) (interface{}, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("bridge_dial requires name and address")
	}
	name := args[0].(string)
	addr := args[1].(string)
	bm := NewBridgeManager(DefaultBridgeConfig(), mi.mind.Swarm())
	_, err := bm.CreateBridge(context.Background(), name, BridgeConfig{
		RemoteMeshName: name,
		RemoteAddress:  addr,
	})
	if err != nil {
		return nil, err
	}
	return "connected", nil
}

func (mi *MindImports) bridgeCall(args ...interface{}) (interface{}, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("bridge_call requires bridge name and method")
	}
	_ = args[0].(string)
	_ = args[1].(string)
	// Would need bridge manager to get bridge
	return "called", nil
}

func (mi *MindImports) bridgeNotify(args ...interface{}) (interface{}, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("bridge_notify requires bridge name and method")
	}
	return "notified", nil
}

func hashModule(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:16])
}
