package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

// EscapeUntrusted escapes every character that could turn an identifier or an
// annotation into markup, a JSON string delimiter or a control sequence. The
// wake and mcp adapters share this table so untrusted text is rendered the
// same way in a wake pointer and in a tool excerpt.
func EscapeUntrusted(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#39;")
		default:
			if r < 0x20 || r == 0x7f {
				b.WriteString(fmt.Sprintf("\\u%04x", r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// RenderEventEnvelope renders a wake pointer as the <beacon-event> text. Every
// field is whitelisted. The explicit id repeats obligationID so an adapter can
// dedup a replay after a client restart (RS-WK-02).
func RenderEventEnvelope(watchID, obligationID string, revision uint64, verdict string) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{
		"id":           obligationID,
		"watchID":      watchID,
		"obligationID": obligationID,
		"revision":     revision,
		"verdict":      verdict,
	})
	return "<beacon-event>" + strings.TrimSpace(buf.String()) + "</beacon-event>"
}
