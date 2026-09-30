package persistence

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

func newTestPM(t *testing.T) *PersistenceManager {
	t.Helper()
	cfg := DefaultPersistenceConfig(t.TempDir())
	cfg.EnableSync = false
	cfg.SaveInterval = 0 // no background loop in tests
	pm, err := NewPersistenceManager(cfg)
	if err != nil {
		t.Fatalf("NewPersistenceManager: %v", err)
	}
	t.Cleanup(func() { _ = pm.Stop() })
	return pm
}

func sampleJob(id string) *hivemind.Job {
	return &hivemind.Job{
		ID:         id,
		Name:       "reconcile-" + id,
		Schedule:   "*/15 * * * *",
		Timeout:    30 * time.Second,
		MaxRetries: 3,
		RetryDelay: 5 * time.Second,
		Enabled:    true,
		CreatedAt:  time.Now().Add(-24 * time.Hour),
	}
}

func TestJobRoundTrip(t *testing.T) {
	pm := newTestPM(t)

	if err := pm.SaveJob(sampleJob("nightly")); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	jobs, err := pm.LoadJobs()
	if err != nil {
		t.Fatalf("LoadJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	j := jobs[0]
	if j.ID != "nightly" || j.Schedule != "*/15 * * * *" || j.MaxRetries != 3 {
		t.Fatalf("job persistence mangled fields: %+v", j)
	}
}

func TestJobDelete(t *testing.T) {
	pm := newTestPM(t)

	if err := pm.SaveJob(sampleJob("ephemeral")); err != nil {
		t.Fatal(err)
	}
	if err := pm.DeleteJob("ephemeral"); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	jobs, err := pm.LoadJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("deleted job still loads: %+v", jobs)
	}
}

func TestTokenRoundTripAndRevoke(t *testing.T) {
	pm := newTestPM(t)

	now := time.Now().Unix()
	tok := &hivemind.Token{
		ID:           "tok-1",
		Subject:      "asia-a",
		Issuer:       "eu-b",
		Audience:     "swarm",
		IssuedAt:     now - 60,
		ExpiresAt:    now + 3600,
		Capabilities: []hivemind.Capability{hivemind.Capability("think")},
	}
	if err := pm.SaveToken(tok); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	tokens, err := pm.LoadTokens()
	if err != nil {
		t.Fatalf("LoadTokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0].Subject != "asia-a" {
		t.Fatalf("token roundtrip wrong: %+v", tokens)
	}

	if err := pm.RevokeToken("tok-1"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	// Revocation is a soft tombstone: the durable ledger must now carry the
	// revoked flag so capability checks can refuse it without deleting audit
	// history.
	raw, err := os.ReadFile(filepath.Join(pm.tokensPath, "tok-1.json"))
	if err != nil {
		t.Fatalf("tombstone file missing: %v", err)
	}
	if !strings.Contains(string(raw), `"revoked": true`) {
		t.Fatalf("revoke did not tombstone the token: %s", raw)
	}
}

func TestBridgeRoundTrip(t *testing.T) {
	pm := newTestPM(t)

	b := &hivemind.BridgeConfig{
		LocalMeshName:  "eu",
		RemoteMeshName: "asia",
		RemoteAddress:  "asia-master:4242",
		AutoReconnect:  true,
		MaxMessageSize: 1 << 20,
	}
	if err := pm.SaveBridge(b); err != nil {
		t.Fatalf("SaveBridge: %v", err)
	}
	bridges, err := pm.LoadBridges()
	if err != nil {
		t.Fatalf("LoadBridges: %v", err)
	}
	if len(bridges) != 1 {
		t.Fatalf("got %d bridges, want 1", len(bridges))
	}
	got := bridges[0]
	if got.RemoteAddress != "asia-master:4242" || !got.AutoReconnect || got.MaxMessageSize != 1<<20 {
		t.Fatalf("bridge persistence mangled fields: %+v", got)
	}
}

func TestFlushAndDirtySurvive(t *testing.T) {
	pm := newTestPM(t)
	if err := pm.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	pm.MarkDirty()
	// Cleanup's single Stop() is the only idempotent way in — calling Stop
	// here as well would double-close the stop channel.
}

func TestStopIsClean(t *testing.T) {
	pm := newTestPM(t)
	if err := pm.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	_ = context.Background()
}
