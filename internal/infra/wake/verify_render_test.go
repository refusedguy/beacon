package wake

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

func t3246Envelope() string {
	return Envelope(domain.WakePayload{
		WatchID:      "w-t3246",
		ObligationID: "ob-t3246",
		Revision:     7,
		Verdict:      "retry",
	})
}

// The capture port is the one from capture.go: the fake runner returns pane
// text, and verification never sends tmux commands of its own.
func TestVerifyRenderDeliveredAfterNormalization(t *testing.T) {
	want := t3246Envelope()
	pane := "\x1b[2J\r\n╭─ session ─╮\r\n" + want + "\r\n❯ \r\n"
	r := &fakeRunner{outs: [][]byte{[]byte(pane)}}

	got, err := VerifyRender(context.Background(), r, "%42", "", want)
	if err != nil {
		t.Fatalf("VerifyRender: %v", err)
	}
	if got.Status != RenderDelivered {
		t.Fatalf("status = %q, want %q (reason: %s)", got.Status, RenderDelivered, got.Reason)
	}

	wantCalls := [][]string{{"capture-pane", "-p", "-t", "%42"}}
	if !reflect.DeepEqual(r.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", r.calls, wantCalls)
	}
}

func TestVerifyRenderFailedOnDifferentPaneText(t *testing.T) {
	want := t3246Envelope()
	r := &fakeRunner{outs: [][]byte{[]byte("unrelated message\n")}}

	got, err := VerifyRender(context.Background(), r, "%42", "", want)
	if err != nil {
		t.Fatalf("VerifyRender: %v", err)
	}
	if got.Status != RenderFailed {
		t.Fatalf("status = %q, want %q (reason: %s)", got.Status, RenderFailed, got.Reason)
	}
}

func TestVerifyRenderUnconfirmedIsNotDeliveryError(t *testing.T) {
	want := t3246Envelope()

	t.Run("capture failure", func(t *testing.T) {
		r := &fakeRunner{errs: []error{errors.New("tmux exited")}}

		got, err := VerifyRender(context.Background(), r, "%42", "", want)
		if err != nil {
			t.Fatalf("capture failure must not be a delivery error: %v", err)
		}
		if got.Status != RenderUnconfirmed {
			t.Fatalf("status = %q, want %q", got.Status, RenderUnconfirmed)
		}
	})

	t.Run("pane without message lines", func(t *testing.T) {
		r := &fakeRunner{outs: [][]byte{[]byte("❯ \n│   │\n")}}

		got, err := VerifyRender(context.Background(), r, "%42", "", want)
		if err != nil {
			t.Fatalf("VerifyRender: %v", err)
		}
		if got.Status != RenderUnconfirmed {
			t.Fatalf("status = %q, want %q", got.Status, RenderUnconfirmed)
		}
	})

	t.Run("only envelope prefix visible", func(t *testing.T) {
		r := &fakeRunner{outs: [][]byte{[]byte(firstRunes(want, renderPrefixRunes) + "\n")}}

		got, err := VerifyRender(context.Background(), r, "%42", "", want)
		if err != nil {
			t.Fatalf("VerifyRender: %v", err)
		}
		if got.Status != RenderUnconfirmed {
			t.Fatalf("status = %q, want %q", got.Status, RenderUnconfirmed)
		}
	})
}

func TestVerifyRenderRetainedWhenPresentBeforeDelivery(t *testing.T) {
	want := t3246Envelope()
	r := &fakeRunner{outs: [][]byte{[]byte(want + "\n")}}

	got, err := VerifyRender(context.Background(), r, "%42", want, want)
	if err != nil {
		t.Fatalf("VerifyRender: %v", err)
	}
	if got.Status != RenderRetained {
		t.Fatalf("status = %q, want %q (reason: %s)", got.Status, RenderRetained, got.Reason)
	}
}
