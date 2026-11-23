// Command cardarchive renders a deterministic archive of closed cards from a
// manifest supplied on stdin. Each manifest line is "ID|Статус|Причина". The
// tool reads no files and runs no processes: the caller reads the cards and
// pipes in the manifest. The archive candidate goes to stdout.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type card struct{ id, status, detail string }

var clean = strings.NewReplacer("\r", " ", "\n", " ", "|", "/")

// parseCard reads one manifest line. Extra pipes stay in the detail so the
// sanitizer can replace them, which keeps one card on one output line.
func parseCard(line string) (card, bool) {
	fields := strings.SplitN(line, "|", 3)
	if len(fields) != 3 {
		return card{}, false
	}
	c := card{
		clean.Replace(strings.TrimSpace(fields[0])),
		clean.Replace(strings.TrimSpace(fields[1])),
		clean.Replace(strings.TrimSpace(fields[2])),
	}
	if c.id == "" || c.detail == "" || (c.status != "done" && c.status != "dropped") {
		return card{}, false
	}
	return c, true
}

// render sorts a copy of the cards by ID and writes one archive row per card.
// The copy keeps repeated calls on the same input byte-identical.
func render(cards []card) []byte {
	ordered := append([]card(nil), cards...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].id != ordered[j].id {
			return ordered[i].id < ordered[j].id
		}
		if ordered[i].status != ordered[j].status {
			return ordered[i].status < ordered[j].status
		}
		return ordered[i].detail < ordered[j].detail
	})
	var b strings.Builder
	for _, c := range ordered {
		fmt.Fprintf(&b, "%s | %s | %s\n", c.id, c.status, c.detail)
	}
	return []byte(b.String())
}

// emit reads the manifest from in and writes the archive candidate to out. It
// returns the number of cards written.
func emit(in io.Reader, out io.Writer) (int, error) {
	data, err := io.ReadAll(in)
	if err != nil {
		return 0, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	var cards []card
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if c, ok := parseCard(line); ok {
			cards = append(cards, c)
		}
	}
	archive := render(cards)
	if _, err := out.Write(archive); err != nil {
		return 0, err
	}
	return bytes.Count(archive, []byte("\n")), nil
}

const selfTestManifest = "T2|done|reason-b\n" +
	"OPEN|todo|skip me\n" +
	"A1|dropped|closed early\n" +
	"\n" +
	"B3|done|pipe | inside\n"

// selfTest runs the built-in manifest twice, checks that both runs match, and
// checks that a changed byte breaks the comparison.
func selfTest(out io.Writer) error {
	var first, second bytes.Buffer
	n, err := emit(strings.NewReader(selfTestManifest), &first)
	if err != nil {
		return err
	}
	if _, err := emit(strings.NewReader(selfTestManifest), &second); err != nil {
		return err
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		return fmt.Errorf("generation is not idempotent")
	}
	corrupt := append([]byte(nil), first.Bytes()...)
	if len(corrupt) == 0 {
		corrupt = []byte("injected mismatch\n")
	} else {
		corrupt[0] = 'X'
	}
	if bytes.Equal(corrupt, first.Bytes()) {
		return fmt.Errorf("injected mismatch was not detected")
	}
	fmt.Fprintf(out, "cardarchive self-test ok: %d cards, idempotent, mismatch detected\n", n)
	return nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "cardarchive: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	emitFlag := flag.Bool("emit", false, "read the manifest from stdin and print the archive candidate to stdout")
	self := flag.Bool("self-test", false, "run built-in fixtures and check idempotency and mismatch detection")
	flag.Parse()

	switch {
	case *self && *emitFlag:
		fatal("choose --emit or --self-test")
	case *self:
		if err := selfTest(os.Stdout); err != nil {
			fatal("%v", err)
		}
	case *emitFlag:
		if _, err := emit(os.Stdin, os.Stdout); err != nil {
			fatal("%v", err)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: cardarchive --emit < manifest | cardarchive --self-test")
		os.Exit(2)
	}
}
