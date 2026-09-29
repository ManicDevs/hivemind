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
	"log"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
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
		if antireInstance != nil && antireInstance.enabled {
			log.Printf("🛡️ [ANTI-RE] initialized (release build)")
		}
	})
	return antireInstance, antireInitErr
}

// newAntiRE creates the AntiRE instance. In non-release builds it returns
// a no-op instance.
func newAntiRE() (*AntiRE, error) {
	log.Printf("🛡️ [ANTI-RE] newAntiRE called, releaseBuild=%v", isReleaseBuild())
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
// at link time (see antire_build.go), or by the `release` build tag alone.
func isReleaseBuild() bool {
	return releaseBuild == "1"
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
		// Debugger sweep: ptrace, timing, and environment probes. A hit one
		// corrupts the keys so every subsequent decryption fails silently.
		if a.detectDebugger() {
			a.debuggerDetected = true
			a.corruptKeysLocked()
			log.Printf("🛡️ [ANTI-RE] debugger sweep hit (ptrace=%v anomalies=%d env=%v) — keys corrupted",
				a.ptraceSeen, a.timingAnomaly, debuggerEnvPresent())
		}
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
		// Additional check: if the leaf key has been zeroed or truncated,
		// that is a strong indicator of debugger interference.
		if len(a.leafKey) == 0 || len(a.leafKey) < 16 {
			a.debuggerDetected = true
			a.corruptKeysLocked()
		}
		// Additional check: verify the key format is valid (32 bytes for SHA-256)
		if len(a.leafKey) > 0 && len(a.leafKey) >= 16 {
			// Check that the key has sufficient entropy (not all zeros, not all ones)
			nonZero := 0
			nonOne := 0
			for _, b := range a.leafKey {
				if b != 0 {
					nonZero++
				}
				if b != 0xFF {
					nonOne++
				}
			}
			if nonZero < 3 || nonOne < 3 {
				a.debuggerDetected = true
				a.corruptKeysLocked()
			}
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
// is likely attached. The result is latched into the per-probe fields so the
// sweep both reacts immediately (keys corrupted by the caller) and records a
// running history for diagnostics.
func (a *AntiRE) detectDebugger() bool {
	detected := false
	// 1. ptrace check: on Linux, ptrace(PTRACE_TRACEME, 0, 0, 0) fails if already traced
	if runtime.GOOS == "linux" {
		if isPtraced() {
			a.ptraceSeen = true
			detected = true
		}
	}

	// 2. Timing check: rdtsc before/after a known operation
	if a.timingCheck() {
		a.timingAnomaly++
		detected = true
	}

	// 3. Check for common debugger env vars / files
	if debuggerEnvPresent() {
		detected = true
	}

	// 4. Timing variance probe: measure jitter on a tight loop.
	// If the attacker single-steps, the per-iteration delta grows beyond
	// the natural variance of the host CPU.
	if a.timingVarianceProbe() {
		a.timingAnomaly++
		detected = true
	}

	// 5. Module/import spy: look for dynamic loading of forbidden packages
	// (cgo, syscall, etc.) at runtime — a debugger may patch imports.
	if a.importSpy() {
		detected = true
	}

	// 6. Hardware register check: inspect MSR/CPUID flags that change under debugger attachment
	if a.hardwareRegisterCheck() {
		detected = true
	}

	// 7. Process environment inspection: check for suspicious env var patterns
	if a.environmentInspection() {
		detected = true
	}

	// 8. File system tampering check: detect unauthorized file modifications
	if a.fileTamperingCheck() {
		detected = true
	}

	return detected
}

// timingVarianceProbe checks whether per-iteration execution time jitter
// has grown beyond natural host variance. A single-stepped debugger inflates
// the delta; we compare the latest interval against a rolling median of
// recent intervals stored in theAntiRE state.
func (a *AntiRE) timingVarianceProbe() bool {
	const probes = 30
	var deltas []float64
	prev := 0.0
	for i := 0; i < probes; i++ {
		start := time.Now()
		_ = i * i
		_ = i + 1
		_ = i | 1
		_ = i & ^1
		_ = math.MaxInt32 - i
		_ = math.MinInt32 + i
		_ = ^uint(0)
		_ = ^^uint(0)
		elapsed := time.Since(start).Seconds()
		if i > 0 {
			deltas = append(deltas, elapsed-prev)
		}
		prev = elapsed
	}
	if len(deltas) < 5 {
		return false
	}
	// compute robust median of all deltas using HAes median (highest average)
	// of consecutive triplets, then take the median of those
	window := 3
	if len(deltas) < window {
		window = 3
	}
	triplets := make([]float64, 0, len(deltas)-2)
	for i := 0; i+2 < len(deltas); i++ {
		triplets = append(triplets, (deltas[i] + deltas[i+1] + deltas[i+2]) / 3.0)
	}
	// sort triplets using simple insertion sort
	for i := 1; i < len(triplets); i++ {
		key := triplets[i]
		j := i - 1
		for ; j >= 0 && triplets[j] > key; j-- {
			triplets[j+1] = triplets[j]
		}
		triplets[j+1] = key
	}
	median := triplets[len(triplets)/2]
	// if the latest delta exceeds median by a factor of 4, suspect debugger
	// but only if we have at least 5 triplets for statistical significance
	if len(triplets) >= 5 && deltas[len(deltas)-1] > 4*median {
		return true
	}
	// also check if the latest delta itself is an extreme outlier
	// (beyond 10x the typical delta range)
	if len(deltas) >= 5 {
		sorted := make([]float64, len(deltas))
		copy(sorted, deltas)
		for i := 1; i < len(sorted); i++ {
			key := sorted[i]
			j := i - 1
			for ; j >= 0 && sorted[j] > key; j-- {
				sorted[j+1] = sorted[j]
			}
			sorted[j+1] = key
		}
		range_val := sorted[len(sorted)-1] - sorted[0]
		if range_val > 0 && (deltas[len(deltas)-1] - sorted[0]) > 10*range_val/float64(len(deltas)) {
			return true
		}
	}
	return false
}

// importSpy checks whether the runtime has dynamically loaded packages that
// are forbidden in a hardened release build. In practice this scans the
// process's import table for cgo/syscall patterns that should not appear.
// A match triggers debugger detection and key corruption. Currently this is
// a placeholder; a pure Go implementation cannot introspect its own import
// table at runtime without cgo, but the framework is prepared for future
// hardening via BPF or DWARF analysis.
func (a *AntiRE) importSpy() bool {
	_ = a
	return false
}

// hardwareRegisterCheck checks for debugger hardware modification indicators.
// In a pure Go implementation, this scans CPU feature flags and model strings
// that may be altered by a debugger. Currently a placeholder; future hardening
// could use BPF or DWARF analysis.
func (a *AntiRE) hardwareRegisterCheck() bool {
	_ = a
	return false
}

// environmentInspection checks for suspicious environment variable patterns
// that may indicate debugger attachment. Currently a pure Go placeholder;
// future hardening could inspect /proc/PID/environ or registry keys.
func (a *AntiRE) environmentInspection() bool {
	debugEnvList := []string{
		"GDB_", "LLDB_", "FRIDA_", "PTRASE", "PROMPT", "XDEBUG_CONFIG",
		"XTRACE", "DYLD_INSERT_LIBRARIES", "LD_PRELOAD",
	}
	for _, e := range os.Environ() {
		for _, env := range debugEnvList {
			if strings.HasPrefix(e, env) {
				return true
			}
		}
	}
	return false
}

// fileTamperingCheck detects unauthorized modifications to critical files.
// In a hardened runtime, this would verify file integrity hashes.
// Currently a placeholder; future hardening could use checksums or BPF.
func (a *AntiRE) fileTamperingCheck() bool {
	_ = a
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

// encryptWithKeyFrom encrypts under the singleton's current leaf key.
func encryptWithKeyFrom(a *AntiRE, plaintext string) (string, error) {
	if a == nil {
		return "", fmt.Errorf("antire: no instance for key access")
	}
	a.mu.RLock()
	key := a.leafKey
	a.mu.RUnlock()
	if len(key) == 0 {
		return "", fmt.Errorf("antire: no leaf key")
	}
	return encryptWithKey(key, plaintext)
}

// decryptWithKeyFrom decrypts under the singleton's current leaf key.
func decryptWithKeyFrom(a *AntiRE, ciphertext string) (string, bool) {
	if a == nil {
		return "", false
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

// EncryptedFunc wraps a function. The wrapped function's identity is carried
// as a ciphertext under the current minute leaf key; the live func value is
// held separately so the wrapper remains callable without unsafe pointer
// arithmetic. The first Call decrypts the ciphertext and verifies it matches
// the held function; a mismatch (key rotation, or debugger-induced key
// corruption) aborts the call with a panic rather than executing through a
// compromised identity.
type EncryptedFunc struct {
	fn         func()
	ciphertext []byte
	once       sync.Once
}

// NewEncryptedFunc wraps fn. In a release build with anti-RE enabled the
// function's pointer is recorded as ciphertext; otherwise the wrapper is a
// plain no-op holder.
func NewEncryptedFunc(fn func()) *EncryptedFunc {
	if fn == nil {
		return &EncryptedFunc{}
	}
	a, _ := GetAntiRE()
	if !a.isEnabled() {
		return &EncryptedFunc{fn: fn}
	}
	ct, _ := encryptWithKeyFrom(a, fmt.Sprintf("%p", fn))
	return &EncryptedFunc{fn: fn, ciphertext: []byte(ct)}
}

// Call runs the wrapped function once its identity has been verified against
// the current key. If the key no longer matches — the minute rotated or a
// debugger corrupted the material — Call panics instead of invoking fn.
func (ef *EncryptedFunc) Call() {
	if ef.fn == nil {
		return
	}
	if ef.ciphertext != nil {
		ef.once.Do(func() {
			a, _ := GetAntiRE()
			plain, ok := decryptWithKeyFrom(a, string(ef.ciphertext))
			if !ok || plain != fmt.Sprintf("%p", ef.fn) {
				panic("antire: EncryptedFunc identity failed verification (key rotated or debugger corruption)")
			}
		})
	}
	ef.fn()
}

// AnnounceKeyPosture logs the current key configuration at startup.
// In release builds, this includes anti-RE status.
func AnnounceKeyPosture() {
	fmt.Fprintf(os.Stderr, "🛡️ [ANTI-RE] runtime hardening active: string encryption, key rotation sync, integrity checks\n")
	a, err := GetAntiRE()
	if err == nil && a != nil && a.isEnabled() {
		// Already logged above
	}
	if RootRotationActive() {
		fmt.Fprintf(os.Stderr, "🔄 [KEYS] root rotation active — listening on %d namespaces so old-root frames are delivered\n", len(Namespaces()))
	}
}
