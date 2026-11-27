// Command bench-gen writes docs/perf/bench-registry.json from the typed table
// in internal/bench. It is the only writer of that file; go:generate in the
// bench package and the drift test both go through EncodeRegistry.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/refusedguy/beacon/internal/bench"
)

func main() {
	out := flag.String("out", "", "path to the bench-registry.json file to write")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "bench-gen: -out is required")
		os.Exit(2)
	}
	data, err := bench.EncodeRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench-gen: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "bench-gen: %v\n", err)
		os.Exit(1)
	}
}
