//go:build release
// +build release

package hivemind

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"debug/elf"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// AntiRE provides runtime anti-reverse-engineering facilities backed by
// the rolling key hierarchy. It has zero cost when not in use and is
// completely disabled in non-release builds.
//
// The design uses the per-minute leaf key (rotating every 60s) for
// ephemeral secrets and the hourly key (rotating every 60m) for
// longer-lived strings. A debugger or analysis tool that pauses execution
// for more than a few minutes finds its decryption keys stale.
//
// Debugger detection corrupts the active key material, causing all
// subsequent decryptions to fail silently. This is the primary defence
// against dynamic analysis: the binary appears to work until it doesn't.

var (
	antireOnce     sync.Once
	antireInstance *AntiRE
	antireInitErr  error
)

// AntiRE holds the runtime anti-RE state.
type AntiRE struct {
	mu               sync.RWMutex
	enabled          bool
	leafKey          []byte // current minute leaf key
	hourKey          []byte // current hour key
	lastLeafMinute   int64
	lastHour         int64
	integrityHash    []byte
	debuggerDetected bool
	ptraceSeen       bool
	timingAnomaly    int
}

// GetAntiRE returns the singleton AntiRE instance, initializing on first use.
func GetAntiRE() (*AntiRE, error) {
	antireOnce.Do(func() {
		antireInstance, antireInitErr = newAntiRE()
	})
	return antireInstance, antireInitErr
}

// newAntiRE creates the AntiRE instance. In non-release builds it returns
// a no-op instance.
func newAntiRE() (*AntiRE, error) {
	a := &AntiRE{enabled: isReleaseBuild()}
	if !a.enabled {
		return a, nil
	}

	// Initial key fetch
	leaf, _, ok := v2LeafKey(0)
	if !ok {
		return nil, fmt.Errorf("antire: no leaf key available")
	}
	hour, _, ok := v2LeafKey(-1) // previous minute ≈ hour boundary
	if !ok {
		return nil, fmt.Errorf("antire: no hour key available")
	}

	a.leafKey = leaf
	a.hourKey = hour
	a.lastLeafMinute = minuteEpoch(time.Now())
	a.lastHour = hourEpoch(time.Now())

	// Compute initial integrity hash of .text section
	if h, err := a.computeTextHash(); err == nil {
		a.integrityHash = h
	}

	// Start background key rotator and integrity checker
	go a.rotator()
	go a.integrityChecker()

	return a, nil
}

// isReleaseBuild reports whether this is a release build. Controlled by
// -ldflags "-X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.releaseBuild=1"
// at link time.
func isReleaseBuild() bool {
	return true
}

// Enable turns on anti-RE features. No-op in non-release builds.
func (a *AntiRE) Enable() {
	if !a.enabled {
		return
	}
	a.mu.Lock()
	a.enabled = true
	a.mu.Unlock()
}

// Disable turns off anti-RE features. Once disabled, cannot be re-enabled
// in the same process (prevents toggle attacks).
func (a *AntiRE) Disable() {
	a.mu.Lock()
	a.enabled = false
	a.mu.Unlock()
}

// ProtectString encrypts a plaintext string with the current hour key and
// returns a base64 string that can be stored in the binary. At runtime,
// UnprotectString will decrypt it using the current hour key. If the hour
// has rotated (or a debugger corrupted the key), decryption fails.
//
// Usage at build time (in a code generator):
//
//	protected, _ := antire.ProtectString("sensitive-api-key")
//	// embed `protected` in the binary as a string literal
//
// At runtime:
//
//	secret, ok := antire.UnprotectString(protected)
func ProtectString(plaintext string) (string, error) {
	a, err := GetAntiRE()
	if err != nil {
		return "", err
	}
	a.mu.RLock()
	key := a.hourKey
	a.mu.RUnlock()
	if len(key) == 0 {
		return "", fmt.Errorf("antire: no hour key")
	}
	return encryptWithKey(key, plaintext)
}

// UnprotectString decrypts a string produced by ProtectString using the
// current hour key. Returns empty string and false on failure (wrong key,
// tampered ciphertext, debugger detected).
func UnprotectString(ciphertext string) (string, bool) {
	a, err := GetAntiRE()
	if err != nil {
		return "", false
	}
	if !a.isEnabled() {
		return ciphertext, true // no-op in non-release
	}
	a.mu.RLock()
	key := a.hourKey
	detected := a.debuggerDetected
	a.mu.RUnlock()
	if detected {
		return "", false
	}
	plain, err := decryptWithKey(key, ciphertext)
	return plain, err == nil
}

// ProtectEphemeral encrypts with the current minute leaf key. Use for
// extremely short-lived secrets (tokens, nonces) that must become
// unreadable within 60 seconds even if the process is paused.
func ProtectEphemeral(plaintext string) (string, error) {
	a, err := GetAntiRE()
	if err != nil {
		return "", err
	}
	a.mu.RLock()
	key := a.leafKey
	a.mu.RUnlock()
	if len(key) == 0 {
		return "", fmt.Errorf("antire: no leaf key")
	}
	return encryptWithKey(key, plaintext)
}

// UnprotectEphemeral decrypts with the current minute leaf key. Fails if
// the minute has rotated since encryption.
func UnprotectEphemeral(ciphertext string) (string, bool) {
	a, err := GetAntiRE()
	if err != nil {
		return "", false
	}
	if !a.isEnabled() {
		return ciphertext, true
	}
	a.mu.RLock()
	key := a.leafKey
	detected := a.debuggerDetected
	a.mu.RUnlock()
	if detected {
		return "", false
	}
	plain, err := decryptWithKey(key, ciphertext)
	return plain, err == nil
}

// isEnabled checks enabled state without holding the lock for long.
func (a *AntiRE) isEnabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.enabled
}

// rotator updates keys on minute/hour boundaries and detects stalled
// execution (debugger pause). Runs every 10 seconds.
func (a *AntiRE) rotator() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if !a.isEnabled() {
			return
		}
		now := time.Now()
		leafMinute := minuteEpoch(now)
		hour := hourEpoch(now)

		a.mu.Lock()
		// Detect debugger pause: if wall-clock advanced >2 minutes but
		// our key rotation hasn't run, we were likely paused in a debugger.
		if leafMinute > a.lastLeafMinute+2 {
			a.debuggerDetected = true
			a.corruptKeysLocked()
		}
		if leafMinute != a.lastLeafMinute {
			if leaf, _, ok := v2LeafKey(0); ok {
				a.leafKey = leaf
			}
			a.lastLeafMinute = leafMinute
		}
		if hour != a.lastHour {
			if hk, _, ok := v2LeafKey(-60); ok { // ~1 hour ago
				a.hourKey = hk
			}
			a.lastHour = hour
		}
		a.mu.Unlock()
	}
}

// integrityChecker periodically verifies .text section integrity.
func (a *AntiRE) integrityChecker() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if !a.isEnabled() {
			return
		}
		h, err := a.computeTextHash()
		if err != nil {
			continue
		}
		a.mu.Lock()
		if len(a.integrityHash) > 0 && !bytesEqual(a.integrityHash, h) {
			a.debuggerDetected = true
			a.corruptKeysLocked()
		}
		a.mu.Unlock()
	}
}

// corruptKeysLocked irreversibly destroys the active keys. Caller must hold lock.
func (a *AntiRE) corruptKeysLocked() {
	// Overwrite with random data
	if len(a.leafKey) > 0 {
		rand.Read(a.leafKey)
	}
	if len(a.hourKey) > 0 {
		rand.Read(a.hourKey)
	}
	a.leafKey = nil
	a.hourKey = nil
}

// computeTextHash hashes the .text section of the running binary.
func (a *AntiRE) computeTextHash() ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	f, err := elf.Open(exe)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var textData []byte
	for _, prog := range f.Progs {
		if prog.Type == elf.PT_LOAD && (prog.Flags&elf.PF_X) != 0 {
			r := prog.Open()
			data, _ := io.ReadAll(r)

			textData = append(textData, data...)
		}
	}
	if len(textData) == 0 {
		return nil, fmt.Errorf("no .text section found")
	}
	sum := sha256.Sum256(textData)
	return sum[:], nil
}

// detectDebugger runs basic anti-debug checks. Returns true if a debugger
// is likely attached.
func (a *AntiRE) detectDebugger() bool {
	// 1. ptrace check: on Linux, ptrace(PTRACE_TRACEME, 0, 0, 0) fails if already traced
	if runtime.GOOS == "linux" {
		if isPtraced() {
			return true
		}
	}

	// 2. Timing check: rdtsc before/after a known operation
	if a.timingCheck() {
		return true
	}

	// 3. Check for common debugger env vars / files
	if debuggerEnvPresent() {
		return true
	}

	return false
}

// isPtraced checks if we're being ptraced.
// In release builds with assembly support, this uses a raw syscall.
// Without assembly, we use a heuristic: check /proc/self/status for TracerPid.
func isPtraced() bool {
	// Heuristic: check if TracerPid != 0 in /proc/self/status
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "TracerPid:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] != "0" {
				return true
			}
		}
	}
	return false
}

// timingCheck measures execution time of a trivial loop. If it takes
// significantly longer than expected, we're likely single-stepped.
func (a *AntiRE) timingCheck() bool {
	start := time.Now()
	// Burn ~10000 cycles
	for i := 0; i < 10000; i++ {
		_ = i * i
	}
	elapsed := time.Since(start)
	// If > 10ms for 10k iterations, we're likely single-stepped
	return elapsed > 10*time.Millisecond
}

func debuggerEnvPresent() bool {
	debugEnvs := []string{
		"GDB_", "LLDB_", "FRIDA_", "R2_", "IDA_", "GHIDRA_",
		"TRACE_", "STRACE_", "LTRACE_", "DEBUGGER_",
	}
	for _, e := range os.Environ() {
		for _, d := range debugEnvs {
			if strings.HasPrefix(e, d) {
				return true
			}
		}
	}
	return false
}

// ========== Low-level crypto helpers ==========

func encryptWithKey(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key[:16]) // use first 16 bytes as AES-128 key
	if err != nil {
		return "", err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aesgcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := aesgcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decryptWithKey(key []byte, ciphertext string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key[:16])
	if err != nil {
		return "", err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := aesgcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ct := data[:nonceSize], data[nonceSize:]
	plain, err := aesgcm.Open(nil, nonce, ct, nil)
	return string(plain), err
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// ========== Syscall stubs (Linux) ==========

const (
	SYS_PTRACE     = 101
	PTRACE_TRACEME = 0
	EPERM          = 1
)

type Errno uintptr

// ========== Code pointer encryption ==========

// EncryptedFunc is a function pointer encrypted with the current leaf key.
// It decrypts itself on first call and re-encrypts after (optional).
// If the key has rotated or debugger corrupted it, the call panics.
type EncryptedFunc struct {
	ciphertext []byte
	decrypted  uintptr
	once       sync.Once
}

func NewEncryptedFunc(fn func()) *EncryptedFunc {
	a, _ := GetAntiRE()
	if !a.isEnabled() {
		return &EncryptedFunc{decrypted: uintptr(unsafe.Pointer(&fn))}
	}
	a.mu.RLock()
	key := a.leafKey
	a.mu.RUnlock()
	ct, _ := encryptWithKey(key, fmt.Sprintf("%p", unsafe.Pointer(&fn)))
	return &EncryptedFunc{ciphertext: []byte(ct)}
}

func (ef *EncryptedFunc) Call() {
	// Placeholder: full encrypted function pointer implementation
	// requires platform-specific runtime code modification.
	panic("antire: EncryptedFunc not implemented")
}
