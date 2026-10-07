package bininspect

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// reportVersion is the schema version stamped onto every export.
//
// Consumers ingesting into a SIEM need to know which shape they received, and a
// bare payload with no version cannot be migrated safely once the struct grows.
// It is bumped when a field is removed or its meaning changes, not for additions.
const reportVersion = "1"

// ExportSchemaVersion is the current report schema version. Exported so a
// consumer can assert against it rather than hard-coding a string.
func ReportSchemaVersion() string { return reportVersion }

// envelope is the SIEM-facing wrapper around a Report.
//
// The indirection is deliberate: a flat Report would force every consumer to
// rediscover which fields matter to them. The envelope carries provenance
// (schema version, generator) alongside the payload, which is what makes a
// stored document self-describing years later.
type envelope struct {
	SchemaVersion string `json:"schema_version"`
	Generator     string `json:"generator"`
	// Format echoes Report.Format so a query can filter without walking in.
	Format string `json:"format"`
	// ExportedAt is omitted when zero, so a zero-time Report does not claim to
	// have been generated at year 1.
	ExportedAt string  `json:"exported_at,omitempty"`
	Report     *Report `json:"report"`
}

// ExportOptions configures JSON serialisation.
type ExportOptions struct {
	// Indent pretty-prints. Leave false for machine ingestion; pretty output
	// roughly doubles the byte count for no benefit to a parser.
	Indent bool
	// Envelope wraps the report in a versioned, self-describing envelope.
	// Leave true unless a consumer has pinned the bare Report shape.
	Envelope bool
	// ExportedAt is stamped into the envelope. Zero means it is omitted, which
	// keeps ExportJSON deterministic for golden-file testing.
	ExportedAt string
	// IncludeZeroScore reports keep a report whose risk score is 0. Leave true;
	// this exists so a consumer can deliberately emit "analysed, nothing found",
	// which is a materially different record from "not analysed".
	IncludeZeroScore bool
}

// ExportJSON serialises the report for ingestion into a SIEM or dashboard.
//
// It is the supported serialisation path: the struct tags define the shape, and
// this defines the envelope, so a consumer never has to reconstruct either.
//
// The error contract is explicit about why a report can be unexportable —
// a nil report or a failed marshal — rather than returning nil bytes and nil
// error on failure.
func (r *UnifiedReport) ExportJSON() ([]byte, error) {
	return r.ExportJSONWith(ExportOptions{Envelope: true})
}

// ExportJSONWith serialises with explicit options. ExportJSON is the common case.
func (r *UnifiedReport) ExportJSONWith(opts ExportOptions) ([]byte, error) {
	if r == nil {
		return nil, errors.New("bininspect: cannot export a nil report")
	}
	// A zero-risk report is a real, meaningful result: "analysed, nothing
	// found". Dropping it silently would be indistinguishable from "not analysed",
	// which is the opposite of what a triage pipeline needs to record.
	if r.Risk.Score == 0 && !opts.IncludeZeroScore {
		return nil, fmt.Errorf("bininspect: refusing to export a zero-risk report (%s); set IncludeZeroScore to record \"analysed, nothing found\"", r.Identity.Name)
	}

	payload := any(r)
	if opts.Envelope {
		payload = envelope{
			SchemaVersion: reportVersion,
			Generator:     "bininspect/" + reportVersion,
			Format:        r.Format,
			ExportedAt:    opts.ExportedAt,
			Report:        r,
		}
	}

	if opts.Indent {
		// Encoder adds a trailing newline, which is what a line-oriented
		// consumer wants; json.MarshalIndent does not.
		var sb strings.Builder
		enc := json.NewEncoder(&sb)
		enc.SetIndent("", "  ")
		// HTML escaping would mangle payloads containing <, >, & — which a
		// section name or a marker string plausibly can.
		enc.SetEscapeHTML(false)
		if err := enc.Encode(payload); err != nil {
			return nil, fmt.Errorf("bininspect: encode report: %w", err)
		}
		return []byte(sb.String()), nil
	}

	// json.Marshal escapes HTML by default; a compact export should not.
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, fmt.Errorf("bininspect: encode report: %w", err)
	}
	// Encode appends a newline even in compact mode; a compact payload should not
	// carry one, so it is trimmed to keep the output byte-stable for hashing.
	return []byte(strings.TrimRight(sb.String(), "\n")), nil
}

// SummaryLine renders the report as a single log line, suitable as a SIEM
// message body where the JSON is carried as a structured field.
//
// It is deliberately not JSON: the point is a human-readable line for a
// dashboard that also has the structured payload.
func (r *UnifiedReport) SummaryLine() string {
	if r == nil {
		return "bininspect: no report"
	}
	var b strings.Builder
	b.WriteString(r.Format)
	b.WriteString(" ")
	b.WriteString(r.Identity.Name)
	fmt.Fprintf(&b, " risk=%d/%s", r.Risk.Score, r.Risk.Rating)
	fmt.Fprintf(&b, " findings=%d", len(r.Findings))
	if r.Stats.Truncated {
		b.WriteString(" TRUNCATED")
	}
	if len(r.Stats.Warnings) > 0 {
		fmt.Fprintf(&b, " warnings=%d", len(r.Stats.Warnings))
	}
	return b.String()
}

// JSONSchemaField describes one exported field, for documentation and for
// consumers that build a parser from the struct rather than hard-coding paths.
type JSONSchemaField struct {
	Path        string `json:"path"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// SchemaFields lists the top-level report fields as they appear in JSON.
//
// It is derived from the struct via reflection so it cannot drift from the real
// shape — a hand-written list would be wrong the first time a tag changed.
func (r *UnifiedReport) SchemaFields() []JSONSchemaField {
	if r == nil {
		return nil
	}
	return describeJSONFields(r)
}

// describeJSONFields walks a struct's JSON tags by reflection.
//
// Deriving the schema from the struct is the point: a hand-maintained field list
// would silently drift the first time a tag changed, which is precisely the bug
// this is meant to prevent.
func describeJSONFields(v any) []JSONSchemaField {
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	out := make([]JSONSchemaField, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name // no tag: encoding/json uses the field name
		}
		desc := ""
		if opts != "" {
			desc = "opts:" + opts
		}
		out = append(out, JSONSchemaField{
			Path:        name,
			Type:        jsonTypeName(f.Type),
			Description: desc,
		})
	}
	return out
}

// jsonTypeName renders a Go type as its JSON type, so the schema description
// matches what a consumer actually receives.
func jsonTypeName(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return "array<" + jsonTypeName(t.Elem()) + ">"
	case reflect.Map:
		return "map<string," + jsonTypeName(t.Elem()) + ">"
	case reflect.Struct:
		return t.Name()
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Interface:
		return "any"
	default:
		return t.Kind().String()
	}
}
