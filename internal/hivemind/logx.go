package hivemind

// RotatingWriter: size-capped log files with retention. When the active
// file exceeds maxBytes, it is renamed to .1 (shifting older generations
// down, dropping beyond keep) and a fresh file begins. Plain files, no
// compression — transcripts stay greppable. Safe for concurrent use.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	file     *os.File
	size     int64
}

// NewRotatingWriter opens path for append, creating parent dirs.
// maxBytes <= 0 disables rotation; keep < 1 keeps one generation.
func NewRotatingWriter(path string, maxBytes int64, keep int) (*RotatingWriter, error) {
	if keep < 1 {
		keep = 1
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &RotatingWriter{path: path, maxBytes: maxBytes, keep: keep, file: f, size: st.Size()}, nil
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.maxBytes > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotatingWriter) rotate() error {
	_ = w.file.Close()
	// Shift generations down: .(keep-1) → .keep … .1 → .2, active → .1.
	for i := w.keep - 1; i >= 1; i-- {
		old := fmt.Sprintf("%s.%d", w.path, i)
		if _, err := os.Stat(old); err == nil {
			_ = os.Rename(old, fmt.Sprintf("%s.%d", w.path, i+1))
		}
	}
	_ = os.Rename(w.path, w.path+".1")
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	w.file = f
	w.size = 0
	// Drop the overflow generation.
	_ = os.Remove(fmt.Sprintf("%s.%d", w.path, w.keep+1))
	return nil
}

// Close flushes and closes the active file.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}
