package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "node.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(writeConfig(t, "---\n"))
	if err != nil {
		t.Fatalf("Load with an empty file must apply defaults: %v", err)
	}
	if c.Node.MaxPeers != 50 {
		t.Fatalf("default max_peers = %d, want 50", c.Node.MaxPeers)
	}
	if c.Mesh.Port != 0 {
		t.Fatalf("default mesh.port = %d, want 0", c.Mesh.Port)
	}
	if c.Storage.MaxBlobSize <= 0 {
		t.Fatalf("default storage.max_blob_size must be positive, got %d", c.Storage.MaxBlobSize)
	}
}

func TestLoadOverlay(t *testing.T) {
	path := writeConfig(t, `
node:
  name: tuner
  max_peers: 7
mesh:
  port: 4242
  enable_dht: false
storage:
  max_blob_size: 1048576
compute:
  max_cpu_percent: 25
`)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Node.Name != "tuner" || c.Node.MaxPeers != 7 {
		t.Fatalf("node overlay not applied: %+v", c.Node)
	}
	if c.Mesh.Port != 4242 || c.Mesh.EnableDHT {
		t.Fatalf("mesh overlay not applied: %+v", c.Mesh)
	}
}

func TestLoadEnvOverride(t *testing.T) {
	path := writeConfig(t, `
node:
  name: file-name
`)
	t.Setenv("HIVEMIND_NODE_NAME", "env-name")
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Node.Name != "env-name" {
		t.Fatalf("env should win over file, got %q", c.Node.Name)
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	paths := []string{
		"node:\n  max_peers: -1\n",                        // non-positive peers
		"mesh:\n  port: 70000\n",                          // out of range port
		"storage:\n  max_blob_size: 0\n",                  // non-positive blob
		"compute:\n  max_cpu_percent: 101\n",              // out of range cpu
		"health:\n  enabled: true\n  listen_addr: \"\"\n", // health on but address explicit-blanked
	}
	for i, content := range paths {
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Fatalf("config #%d should fail validation:\n%s", i+1, content)
		}
	}
}

func TestPathHelpersAreSane(t *testing.T) {
	c, err := Load(writeConfig(t, "---\n"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVEMIND_NODE_DATA_DIR", "must-not-leak") // helper paths stay derived, not env-shaped
	c, err = Load(writeConfig(t, "---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir() == "" || c.SoulDir() == "" || c.BlobPath() == "" {
		t.Fatalf("path helpers must resolve non-empty paths")
	}
	if filepath.Base(c.SoulDir()) != "souls" {
		t.Fatalf("soul dir base = %q, want souls", filepath.Base(c.SoulDir()))
	}
}
