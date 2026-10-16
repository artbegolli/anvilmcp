package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

const (
	// maxArgsJSONBytes caps a stored args blob. Past it the blob is replaced
	// by a digest plus the key names, which keeps the record valid JSON and
	// still lets you prove which call this was.
	maxArgsJSONBytes = 16 * 1024

	// maxStringBytes caps any single string value. fs.write content and
	// kernel.exec code are the ones that run long.
	maxStringBytes = 4 * 1024

	// maxPreviewBytes caps a stored stdout/stderr preview.
	maxPreviewBytes = 16 * 1024

	maxDepth    = 8
	maxElements = 64

	redactedMarker = "[REDACTED]"
)

// redactArgs renders call arguments as JSON suitable for storage: keys that
// look secret-bearing under env.redact_patterns are replaced, long values are
// truncated, and the whole blob is size-capped.
//
// This is a storage hygiene measure, not a guarantee. A secret passed as a
// positional value under an innocuous key (argv, say) is recorded as given —
// which is the point of an audit trail, and why the history database is
// created 0700.
func (r *Recorder) redactArgs(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}

	cleaned, _ := r.redactValue("", args, 0).(map[string]any)
	b, err := json.Marshal(cleaned)
	if err != nil {
		return `{"_audit_error":"arguments were not serializable"}`
	}
	if len(b) <= maxArgsJSONBytes {
		return string(b)
	}

	sum := sha256.Sum256(b)
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	summary, err := json.Marshal(map[string]any{
		"_audit_truncated": true,
		"_audit_bytes":     len(b),
		"_audit_sha256":    hex.EncodeToString(sum[:]),
		"_audit_keys":      keys,
	})
	if err != nil {
		return `{"_audit_error":"arguments exceeded the size cap"}`
	}
	return string(summary)
}

func (r *Recorder) redactValue(key string, v any, depth int) any {
	if depth > maxDepth {
		return "[depth limit]"
	}
	if key != "" && r.engine != nil && r.engine.ShouldRedact(key) {
		return redactedMarker
	}

	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = r.redactValue(k, val, depth+1)
		}
		return out

	case map[string]string:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = r.redactValue(k, val, depth+1)
		}
		return out

	case []any:
		out := make([]any, 0, min(len(t), maxElements))
		for i, val := range t {
			if i >= maxElements {
				out = append(out, fmt.Sprintf("[%d more elided]", len(t)-maxElements))
				break
			}
			// Elements inherit the slice's key so argv under a redacted key
			// stays redacted elementwise.
			out = append(out, r.redactValue(key, val, depth+1))
		}
		return out

	case []string:
		out := make([]any, 0, min(len(t), maxElements))
		for i, val := range t {
			if i >= maxElements {
				out = append(out, fmt.Sprintf("[%d more elided]", len(t)-maxElements))
				break
			}
			out = append(out, truncate(val, maxStringBytes))
		}
		return out

	case string:
		return truncate(t, maxStringBytes)

	default:
		return v
	}
}

func preview(s string) string {
	return truncate(s, maxPreviewBytes)
}

// truncate shortens s to at most limit bytes without splitting a UTF-8 rune,
// appending a marker that says how much was dropped.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s…[truncated, %d bytes total]", s[:cut], len(s))
}

// utf8Start reports whether b begins a UTF-8 sequence (i.e. is not a 10xxxxxx
// continuation byte).
func utf8Start(b byte) bool {
	return b&0xC0 != 0x80
}
