package hivemind

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T, cfg *StorageConfig) (string, *SoulStore) {
	t.Helper()
	base := t.TempDir()
	node := "test-node"
	c := DefaultStorageConfig()
	c.BasePath = base
	c.EnableSnapshots = false
	c.CrossNodeSync = false
	c.IntegrityCheck = true
	if cfg != nil {
		c.IntegrityCheck = cfg.IntegrityCheck
		c.Compression = cfg.Compression
		c.CompressionLevel = cfg.CompressionLevel
	}
	ss, err := NewSoulStore(node, c)
	if err != nil {
		t.Fatalf("NewSoulStore: %v", err)
	}
	t.Cleanup(func() { _ = ss.Stop() })
	return filepath.Join(base, node), ss
}

func sampleMemory(name string) Memory {
	return Memory{
		IdentitySeed: name,
		LivesLived:   3,
		Fitness:      0.82,
		LastThought:  "endurance is also a sacrament",
		KnownPeers:   map[string]bool{"asia-a": true, "eu-b": true},
		DeathPain:    0.4,
	}
}

func TestSoulRoundTripEnvelope(t *testing.T) {
	path, ss := newTestStore(t, nil)

	if err := ss.Save(context.Background(), "recall", sampleMemory("recall")); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(path, "recall.soul"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var env soulEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("durable file is not an envelope: %v", err)
	}
	if env.Checksum == "" || len(env.Payload) == 0 {
		t.Fatalf("envelope missing checksum/payload")
	}

	got, err := ss.Load(context.Background(), "recall")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.IdentitySeed != "recall" {
		t.Fatalf("roundtrip identity_seed = %q", got.IdentitySeed)
	}
}

func TestSoulLoadDetectsTornWrite(t *testing.T) {
	_, ss := newTestStore(t, nil)

	if err := ss.Save(context.Background(), "torn", sampleMemory("torn")); err != nil {
		t.Fatalf("save: %v", err)
	}
	path := filepath.Join(ss.basePath, "torn.soul")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corruptPayloadByte(raw) // flip a payload byte to another ASCII char: JSON stays valid, the hash must catch it
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := ss.Load(context.Background(), "torn"); err == nil {
		t.Fatalf("expected integrity failure for corrupted soul, got nil")
	}
}

func TestSoulLegacyRawFileStillLoads(t *testing.T) {
	path, ss := newTestStore(t, nil)

	// A legacy raw-compressed soul predates envelopes; it must still load
	// (no outside checksum exists to verify, so integrity is skipped, not
	// failed) — otherwise the mesh wakes up forgetting every pre-upgrade life.
	raw, err := json.Marshal(sampleMemory("legacy"))
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := ss.compress(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "legacy.soul"), compressed, 0644); err != nil {
		t.Fatal(err)
	}

	got, err := ss.Load(context.Background(), "legacy")
	if err != nil {
		t.Fatalf("legacy soul should load: %v", err)
	}
	if got.IdentitySeed != "legacy" {
		t.Fatalf("legacy roundtrip identity_seed = %q", got.IdentitySeed)
	}
}

func TestVerifyIntegrityFlagsCorruption(t *testing.T) {
	path, ss := newTestStore(t, nil)

	if err := ss.Save(context.Background(), "good", sampleMemory("good")); err != nil {
		t.Fatal(err)
	}
	if err := ss.Save(context.Background(), "bad", sampleMemory("bad")); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(path, "bad.soul"))
	if err != nil {
		t.Fatal(err)
	}
	corruptPayloadByte(raw) // flip a payload byte to another ASCII char (JSON-valid, hash-invalid)
	if err := os.WriteFile(filepath.Join(path, "bad.soul"), raw, 0644); err != nil {
		t.Fatal(err)
	}

	out, _ := captureStderr(func() { ss.verifyIntegrity() })
	if !strings.Contains(out, "integrity FAILED") || !strings.Contains(out, "bad.soul") {
		t.Fatalf("expected the bad soul to be flagged on stderr, got: %q", out)
	}
	if strings.Contains(out, "good.soul") {
		t.Fatalf("good soul must not be flagged: %q", out)
	}
}

// corruptPayloadByte rewrites the last-occurring payload-zone byte to another
// ASCII character. The envelope JSON stays syntactically valid and UTF-8-clean,
// so only the sha256 check can catch the tamper — a UTF-8-breaking XOR would
// make the file undecodable as JSON and it would be skipped as a legacy soul,
// which tests the fallback path, not the checksum.
func corruptPayloadByte(raw []byte) {
	for i := len(raw) * 3 / 4; i < len(raw); i++ {
		if raw[i] != 'A' {
			raw[i] = 'A'
			return
		}
	}
}

// captureStderr reruns fn with os.Stderr swapped for a pipe and returns what
// the function wrote, so telemetry can be asserted without polluting output.
func captureStderr(fn func()) (string, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	fn()
	_ = w.Close()
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	return string(buf[:n]), nil
}
