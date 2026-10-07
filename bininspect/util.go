package bininspect

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// formatFloat renders a float to two decimals without a trailing exponent,
// which keeps report JSON and log lines stable for human diffing.
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', 2, 64)
}

// dedupeEvidence removes duplicate evidence entries while preserving order.
// Callers accumulate evidence from several passes over the same import table,
// and a repeated artifact in a report reads as two independent signals.
func dedupeEvidence(in []Evidence) []Evidence {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]Evidence, 0, len(in))
	for _, e := range in {
		k := e.Kind + "\x00" + e.Section + "\x00" + e.Detail
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

// identify hashes a reader into a FileIdentity.
//
// MD5 is computed for legacy intelligence-key compatibility only and is
// explicitly flagged as broken in the result. SHA-256 is the field to use for
// integrity decisions.
//
// A single io.Copy pass feeds both hashes, so the file is read once and no full
// copy is buffered in memory.
func identify(name string, r io.Reader, modTime time.Time) (FileIdentity, error) {
	md5h := md5.New()
	sha := sha256.New()
	n, err := io.Copy(io.MultiWriter(md5h, sha), r)
	if err != nil {
		return FileIdentity{}, err
	}
	return FileIdentity{
		Name:                       name,
		Size:                       n,
		ModTime:                    modTime,
		MD5:                        hex.EncodeToString(md5h.Sum(nil)),
		MD5CryptographicallyBroken: true,
		SHA256:                     hex.EncodeToString(sha.Sum(nil)),
	}, nil
}

// normalizeSymbolName strips decoration from an imported symbol name.
//
// Some linkers and obfuscators append a suffix, or pack a NUL-truncated name
// into the import table. Lowercasing makes signature matching
// case-insensitive, which matters because the same API can appear as
// "Sleep", "sleep", or "SLEEP" in different toolchains.
func normalizeSymbolName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// containsAny reports whether s contains any of the needles, case-insensitively.
// Used for raw-byte/string probes where the exact case is not meaningful.
func containsAny(haystack string, needles []string) bool {
	h := strings.ToLower(haystack)
	for _, n := range needles {
		if strings.Contains(h, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// humanBytes renders a size for a report line.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
