package main

import (
	"log/slog"
	"os"
	"strings"
)

// newLogger builds the node's base structured logger.
//
// Generation 1 mutation: the fabric used stdlib log.Printf for all 20+ of its
// lines, formatting node identity into strings ("[link] ↑ c2.t1 w=0.873").
// That makes a cross-continent sweep for latency spikes or refused
// connections a regex instead of a filter — the exact anomaly class that a
// structured log makes trivial. Every caller now passes slog attrs so the
// mesh's logs can be joined on node_id / peer / loss.
//
// FABRIC_LOG_LEVEL tunes the floor (debug|info|warn|error). A node whose
// world-report sensor has gone quiet must not also go quiet itself.
func newLogger(node *NodeID) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FABRIC_LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	base := slog.New(h)
	if node == nil {
		return base
	}
	// Every line this node emits carries its own identity, so a line can be
	// attributed without reading the message text.
	return base.With(slog.String("node_id", node.String()))
}
