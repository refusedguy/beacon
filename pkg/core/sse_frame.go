package core

import (
	"bufio"
	"io"
	"strings"
)

// The stream parser uses a 64 KB starting buffer and a 4 MB token limit. Both
// readers need the same bounds, so they live here rather than at each caller.
const (
	sseInitialBuffer = 64 * 1024
	sseMaxToken      = 4 << 20
)

// SSEFrame is one parsed server-sent event frame. ID is carried for readers
// that resume a stream; readers that do not resume simply ignore it.
type SSEFrame struct {
	ID    string
	Event string
	Data  string
}

// SSEEOFPolicy decides what happens to a frame left open when the stream ends.
type SSEEOFPolicy bool

const (
	// SSEDropPartial discards a frame that has no terminating blank line.
	SSEDropPartial SSEEOFPolicy = false
	// SSEKeepPartial emits a frame that has no terminating blank line.
	SSEKeepPartial SSEEOFPolicy = true
)

// ParseSSE reads server-sent event frames from r and calls onFrame for each
// complete frame. A blank line closes a frame. Lines beginning with ":" are
// comments. A field name and value split on the first colon, and one leading
// space is removed from the value. Repeated data fields join with a newline.
// The EOF policy is explicit because the two readers disagree: the OpenCode
// stream keeps a final open frame, while the loopback stream drops it.
func ParseSSE(r io.Reader, eof SSEEOFPolicy, onFrame func(SSEFrame)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, sseInitialBuffer), sseMaxToken)
	var frame SSEFrame
	emit := func() {
		if frame.ID != "" || frame.Event != "" || frame.Data != "" {
			onFrame(frame)
		}
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			emit()
			frame = SSEFrame{}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value := line, ""
		if i := strings.IndexByte(line, ':'); i >= 0 {
			field, value = line[:i], strings.TrimPrefix(line[i+1:], " ")
		}
		switch field {
		case "id":
			frame.ID = value
		case "event":
			frame.Event = value
		case "data":
			if frame.Data != "" {
				frame.Data += "\n"
			}
			frame.Data += value
		}
	}
	if eof == SSEKeepPartial {
		emit()
	}
	return scanner.Err()
}
