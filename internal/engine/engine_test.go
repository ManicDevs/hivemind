package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeDefaults(t *testing.T) {
	var c Config
	if err := c.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if c.Mode != "standalone" {
		t.Errorf("mode = %q, want standalone", c.Mode)
	}
	if c.Node != "local" {
		t.Errorf("node = %q, want local", c.Node)
	}
	if len(c.Minds) != 3 || c.Minds[0] != "Alpha" {
		t.Errorf("minds = %v, want the trinity", c.Minds)
	}
}

func TestNormalizeRejectsBadMode(t *testing.T) {
	c := Config{Mode: "cluster"}
	if err := c.Normalize(); err == nil {
		t.Fatal("expected error for unknown mode")
	} else if !strings.Contains(err.Error(), "standalone|peer") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestLoadJSONOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.json")
	raw := `{"mode":"peer","node":"winterfell-alpha","minds":["Raven","Wolf"],"health_addr":"127.0.0.1:9091","law_journal":"/tmp/north.winterfell.journal"}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var c Config
	c.JSONPath = path
	if err := c.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if c.Mode != "peer" || c.Node != "winterfell-alpha" || c.HealthAddr != "127.0.0.1:9091" {
		t.Errorf("overlay not applied: %+v", c)
	}
	if len(c.Minds) != 2 || c.Minds[1] != "Wolf" {
		t.Errorf("minds = %v, want file's pair", c.Minds)
	}
	if c.LawJournal != "/tmp/north.winterfell.journal" {
		t.Errorf("law_journal = %q, want the file's path", c.LawJournal)
	}
}

func TestLoadJSONMissingFile(t *testing.T) {
	var c Config
	c.JSONPath = filepath.Join(t.TempDir(), "nope.json")
	if err := c.Normalize(); err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	want := Config{Mode: "peer", Node: "north", Minds: []string{"A"}}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Config
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	// Node/Mode/Minds survive JSON; HealthAddr/JSONPath zero by design.
	if got.Mode != "peer" || got.Node != "north" || got.Minds[0] != "A" {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

// TestLifecycle births a real universe in standalone mode (no mesh, no
// network) and proves Start/Stop are idempotent-safe and Stats reflect what
// happened. Slow models are irrelevant here: language is env-gated and this
// environment has none wired in explicitly.
func TestLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("lifecycle birth is slow for -short")
	}
	eng, err := New(Config{Mode: "standalone"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)

	stats := eng.Stats()
	if stats.Node != "local" || stats.Mode != "standalone" {
		t.Errorf("stats node/mode = %q/%q", stats.Node, stats.Mode)
	}
	if stats.Minds != 3 || !stats.Overmind {
		t.Errorf("stats = %+v, want 3 minds + overmind", stats)
	}
	if stats.Uptime <= 0 {
		t.Errorf("uptime = %v, want positive", stats.Uptime)
	}

	if err := eng.Start(context.Background()); err == nil {
		t.Error("double Start should fail")
	}
	eng.Stop()
	eng.Stop() // idempotent
	select {
	case <-eng.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("engine did not finish stopping")
	}
}
