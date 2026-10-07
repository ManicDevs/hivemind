package environment

// Shared helpers, deliberately free of imports.
//
// The probe files avoid the standard library wherever a dozen lines would do,
// because every import here is a surface a reviewer has to trust. Strings and
// strconv would cover most of this; these are the few operations actually used.

// itoa renders a non-negative-or-negative int64 without strconv.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ftoa renders a float to two decimals, matching the reporting style elsewhere.
func ftoa(f float64) string {
	scaled := int64(f*100 + 0.5)
	if f < 0 {
		scaled = -scaled
	}
	neg := f < 0
	out := itoa(scaled/100) + "." + twoDigits(scaled%100)
	if neg && scaled != 0 {
		return "-" + out
	}
	return out
}

func twoDigits(n int64) string {
	if n < 0 {
		n = -n
	}
	return string([]byte{byte('0' + n/10%10), byte('0' + n%10)})
}

// containsFold is a case-insensitive substring test.
func containsFold(haystack, needle string) bool {
	h, n := []byte(haystack), []byte(needle)
	if len(n) == 0 {
		return true
	}
	if len(n) > len(h) {
		return false
	}
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for j := range n {
			if lower(h[i+j]) != lower(n[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func lower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 32
	}
	return b
}

// trimSpace removes surrounding ASCII whitespace.
func trimSpace(s string) string {
	start := 0
	for start < len(s) && isSpace(s[start]) {
		start++
	}
	end := len(s)
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

// splitLines splits on '\n' without importing strings.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// sortedKeys gives deterministic iteration over a map, so evidence ordering is
// stable across runs. Without this the same host yields different JSON on each
// run, which defeats golden-file testing.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Insertion sort: the maps here are tiny and this keeps the file import-free.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
