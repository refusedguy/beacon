package main

import "testing"

// adr0016MandatoryCommands are the subcommands ADR 0016 requires in the
// machine-readable catalog. A dispatcher that runs a command but leaves it out
// of `beacon list --format json` breaks the contract: agents read the catalog
// and cannot discover the command (BCX-68).
var adr0016MandatoryCommands = []string{"build", "test", "gates", "list", "doctor", "what"}

func TestADR0016MandatoryCommandsInCatalogAndDispatch(t *testing.T) {
	catalogNames := make(map[string]command, len(catalog))
	for _, c := range catalog {
		catalogNames[c.Name] = c
	}

	for _, name := range adr0016MandatoryCommands {
		entry, ok := catalogNames[name]
		if !ok {
			t.Errorf("команда %s обязательна по ADR 0016, но отсутствует в каталоге", name)
			continue
		}
		if entry.Summary == "" || entry.Usage == "" {
			t.Errorf("команда %s в каталоге без summary или usage: %+v", name, entry)
		}
		if runnerFor[name] == nil {
			t.Errorf("команда %s есть в каталоге, но отсутствует в диспетчере", name)
		}
	}
}
