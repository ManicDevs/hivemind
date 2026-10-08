package platform

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestSerialConsoleNamesUART ensures the backend self-identifies as a serial
// device rather than as stdout.
func TestSerialConsoleNamesUART(t *testing.T) {
	var buf bytes.Buffer
	c := NewSerialConsole(&buf)
	if c.Name() != "uart0" {
		t.Fatalf("Name = %q, want uart0", c.Name())
	}
	if _, err := c.WriteString("ok"); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "ok" {
		t.Fatalf("console did not forward write: %q", buf.String())
	}
}

// TestSerialPathWrites verifies end-to-end console output to a host path.
func TestSerialPathWrites(t *testing.T) {
	dir := t.TempDir()
	device := filepath.Join(dir, "ttyS0")
	p, err := NewSerialPath(device, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "serial" {
		t.Fatalf("Name = %q, want serial", p.Name())
	}
	if _, err := p.Console().WriteString("hello\n"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(device)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello\n" {
		t.Fatalf("serial device received %q, want %q", string(b), "hello\n")
	}
	closeSerial(p)
}

func closeSerial(p *Serial) {
	if sc, ok := p.console.(*SerialConsole); ok && sc.w != nil {
		if c, ok := sc.w.(io.Closer); ok {
			_ = c.Close()
		}
	}
}
