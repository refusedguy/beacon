// Package eventbus keeps one undecodable event from stalling a replay. A
// decoder rejects a bad payload, the guard records where it happened, and the
// replay continues from the next sequence number. The package holds no bus and
// reads no storage. It only implements the checkpoint logic (S2.004).
package eventbus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// RawEvent is one event before its payload has been decoded.
type RawEvent struct {
	Seq     uint64
	Type    string
	Payload []byte
}

// Poison records the most recent event whose payload could not be decoded.
// Seq locates the event in the journal. PayloadSHA identifies the exact bytes
// without keeping the bad payload itself.
type Poison struct {
	Seq        uint64 `json:"seq"`
	PayloadSHA string `json:"payload_sha"`
}

// Checkpoint is a replay position plus the most recent poison. Watermark is
// the highest seq the replay has consumed, poisoned or not.
type Checkpoint struct {
	Watermark  uint64  `json:"watermark"`
	LastPoison *Poison `json:"last_poison,omitempty"`
}

// NextSeq is the seq a caller reads after this checkpoint. Because the
// watermark advances past a poisoned event, the caller never retries it.
func (c Checkpoint) NextSeq() uint64 {
	return c.Watermark + 1
}

// Decoder turns a raw payload into a value. A non-nil error marks the event as
// poison. The returned value is handed to the consumer when decoding succeeds.
type Decoder func(payload []byte) (any, error)

// JSONDecoder decodes a payload as JSON. Invalid JSON marks the event as
// poison.
func JSONDecoder(payload []byte) (any, error) {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// Delivered is one event whose payload decoded successfully.
type Delivered struct {
	Seq   uint64
	Type  string
	Value any
}

// Result is the outcome of consuming a page of raw events.
type Result struct {
	Delivered  []Delivered
	Checkpoint Checkpoint
}

// Process consumes events starting from cp. Each event goes through decode. A
// decode failure replaces LastPoison but does not stop the loop. Watermark
// advances past every event, so a replay resumes at NextSeq and a single bad
// payload cannot block the events behind it. A nil decode uses JSONDecoder.
func Process(cp Checkpoint, events []RawEvent, decode Decoder) Result {
	if decode == nil {
		decode = JSONDecoder
	}
	out := Result{
		Delivered:  make([]Delivered, 0, len(events)),
		Checkpoint: cp,
	}
	for _, event := range events {
		value, err := decode(event.Payload)
		if err != nil {
			out.Checkpoint.LastPoison = &Poison{
				Seq:        event.Seq,
				PayloadSHA: PayloadSHA(event.Payload),
			}
		} else {
			out.Delivered = append(out.Delivered, Delivered{
				Seq:   event.Seq,
				Type:  event.Type,
				Value: value,
			})
		}
		if event.Seq > out.Checkpoint.Watermark {
			out.Checkpoint.Watermark = event.Seq
		}
	}
	return out
}

// PayloadSHA returns the hex encoded SHA-256 of the payload bytes.
func PayloadSHA(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
