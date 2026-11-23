package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestEmitRendersClosedCardsInIDOrder(t *testing.T) {
	in := "T2|done|reason-b\n" +
		"OPEN|todo|skip me\n" +
		"A1|dropped|closed early\n" +
		"\n"
	var out bytes.Buffer
	n, err := emit(strings.NewReader(in), &out)
	if err != nil {
		t.Fatal(err)
	}
	want := "A1 | dropped | closed early\nT2 | done | reason-b\n"
	if got := out.String(); got != want {
		t.Fatalf("emit: got %q want %q", got, want)
	}
	if n != 2 {
		t.Fatalf("card count: got %d want 2", n)
	}
}

func TestEmitSanitizesDetail(t *testing.T) {
	var out bytes.Buffer
	if _, err := emit(strings.NewReader("A|done|a | b\r\nc\n"), &out); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "A | done | a / b c\n"; got != want {
		t.Fatalf("sanitized detail: got %q want %q", got, want)
	}
}

func TestEmitSkipsMalformedLines(t *testing.T) {
	in := "A|done\n" +
		"|done|no id\n" +
		"B|in-progress|skip\n" +
		"B|done|keep\n"
	var out bytes.Buffer
	if _, err := emit(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "B | done | keep\n"; got != want {
		t.Fatalf("malformed lines: got %q want %q", got, want)
	}
}

func TestEmitIsDeterministicAcrossInputOrders(t *testing.T) {
	inputs := []string{
		"B|done|b\nA|dropped|a\n",
		"A|dropped|a\nB|done|b\n",
	}
	var outputs []string
	for _, in := range inputs {
		var out bytes.Buffer
		if _, err := emit(strings.NewReader(in), &out); err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, out.String())
	}
	if outputs[0] != outputs[1] {
		t.Fatalf("input order changed output: %q vs %q", outputs[0], outputs[1])
	}
}

func TestEmitIsIdempotent(t *testing.T) {
	in := "Z|done|z\nA|dropped|a\n"
	var first, second bytes.Buffer
	if _, err := emit(strings.NewReader(in), &first); err != nil {
		t.Fatal(err)
	}
	if _, err := emit(strings.NewReader(in), &second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("emit is not byte-idempotent")
	}
}

func TestSelfTestPassesOnFixtures(t *testing.T) {
	var out bytes.Buffer
	if err := selfTest(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "cardarchive self-test ok: 3 cards") {
		t.Fatalf("self-test output: %q", out.String())
	}
}
