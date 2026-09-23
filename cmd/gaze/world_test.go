package main

// TestWorldLive raises a real, multi-continent HIVEMIND world on this one
// system and proves it from the outside: binaries built, nodes linked,
// frames flowing, and rolling keys live. One command:
//     go test ./cmd/gaze -run TestWorldLive -v
// Everything is spawned as real subprocesses on unix sockets — no mocks.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/entropy"
)

type gazeVitals struct {
	FramesTotal int            `json:"frames_total"`
	LinkedPeers int            `json:"linked_peers"`
	HourKey     string         `json:"hour_key"`
	ByKind      map[string]int `json:"by_kind"`
	Node        string         `json:"node"`
}

func TestWorldLive(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}

	// Relay key baked in exactly like `make build` does, so the rolling
	// keys are provable, not blank.
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for root != "" && root != "/" {
		if _, e := os.Stat(filepath.Join(root, "go.mod")); e == nil {
			break
		}
		root = filepath.Dir(root)
	}
	cwd, _ := os.Getwd()
	if b, e := os.ReadFile(filepath.Join(root, "go.mod")); e != nil {
		t.Fatalf("module root not found from %q: %v", cwd, e)
	} else if !strings.Contains(string(b), "hivemind") {
		t.Fatalf("go.mod at %s is not hivemind", root)
	}
	relayKey := ""
	if b, err := os.ReadFile(filepath.Join(root, ".relaykey")); err == nil {
		relayKey = strings.TrimSpace(string(b))
	}
	if relayKey == "" {
		rb, err := entropy.Hash(32)
		if err != nil {
			t.Fatalf("entropy.Hash: %v", err)
		}
		relayKey = hex.EncodeToString(rb)
		_ = os.WriteFile(filepath.Join(root, ".relaykey"), []byte(relayKey+"\n"), 0600)
	}
	ldflags := ""
	if relayKey != "" {
		ldflags = "-X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=" + relayKey
	}

	binDir := t.TempDir()
	build := func(pkg, name string) {
		args := []string{"build"}
		if ldflags != "" {
			args = append(args, "-ldflags", ldflags)
		}
		args = append(args, "-o", filepath.Join(binDir, name), pkg)
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", pkg, err, b)
		}
	}
	build("./cmd/hivemind", "hivemind")
	build("./cmd/gaze", "gaze")

	// A small world: two continents, one master + one peer each.
	world := []struct{ name string }{
		{"eu-master-1"}, {"eu-peer-1"}, {"as-master-1"}, {"as-peer-1"},
	}
	var procs []*exec.Cmd
	defer func() {
		for _, c := range procs {
			_ = c.Process.Kill()
		}
		time.Sleep(300 * time.Millisecond)
		matches, _ := filepath.Glob("/tmp/hivemind-worldt-*.sock")
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}()

	env := append(os.Environ(),
		"HIVEMIND_RELAY=off",
		"HIVEMIND_TICK_MS=200",
	)
	workdir := t.TempDir()
	start := func(name, bin string, args ...string) {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		cmd.Dir = workdir
		f, _ := os.Create(filepath.Join(binDir, name+".log"))
		cmd.Stdout = f
		cmd.Stderr = f
		if err := cmd.Start(); err != nil {
			t.Fatalf("start %s: %v", name, err)
		}
		procs = append(procs, cmd)
	}

	hmBin := filepath.Join(binDir, "hivemind")
	for _, n := range world {
		start(n.name, hmBin, "-mode", "peer", "-node", n.name)
	}
	time.Sleep(6 * time.Second) // let the mesh link

	gazeBin := filepath.Join(binDir, "gaze")
	gazePort := 18099
	start("gaze", gazeBin, "-serve", fmt.Sprintf("127.0.0.1:%d", gazePort))
	time.Sleep(3 * time.Second)

	stateURL := fmt.Sprintf("http://127.0.0.1:%d/state", gazePort)

	var st struct {
		Gaze  gazeVitals `json:"gaze"`
		Alive []string   `json:"alive"`
	}
	deadline := time.Now().Add(45 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("world never linked: last state frames=%d linked=%d hour_key=%q",
				st.Gaze.FramesTotal, st.Gaze.LinkedPeers, st.Gaze.HourKey)
		}
		resp, err := http.Get(stateURL)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err := json.Unmarshal(b, &st); err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		if st.Gaze.FramesTotal > 0 && st.Gaze.LinkedPeers > 0 && st.Gaze.HourKey != "" {
			break
		}
		time.Sleep(2 * time.Second)
	}

	if len(st.Alive) < 4 {
		t.Errorf("expected ≥4 alive nodes, saw %d", len(st.Alive))
	}
	if st.Gaze.LinkedPeers == 0 {
		t.Error("no peers linked")
	}
	if st.Gaze.FramesTotal == 0 {
		t.Error("no frames witnessed")
	}
	if st.Gaze.HourKey == "" {
		t.Error("rolling hour key not derived (build without -ldflags?)")
	}

	// Push a command as an equal node — proof any master or peer is an
	// entrance, gateway-free.
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/command", gazePort),
		"application/json",
		strings.NewReader(`{"kind":"genesis","payload":"Curiosity"}`))
	if err != nil {
		t.Fatalf("command push: %v", err)
	}
	var reply struct {
		Accepted bool `json:"accepted"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&reply)
	resp.Body.Close()
	if !reply.Accepted {
		t.Error("command rejected by the mesh")
	}

	t.Logf("WORLD LIVE: nodes=%d linked=%d frames=%d by_kind=%v hour_key=%s…",
		len(st.Alive), st.Gaze.LinkedPeers, st.Gaze.FramesTotal, st.Gaze.ByKind, st.Gaze.HourKey[:12])
}
