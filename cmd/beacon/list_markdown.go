package main

import (
	"fmt"
	"io"
	"strings"
)

// escapeMarkdownCell escapes characters that would break a markdown table row.
// Catalog usage strings carry '|' in --format values such as table|json.
func escapeMarkdownCell(cell string) string {
	return strings.ReplaceAll(cell, "|", `\|`)
}

// writeCommandMarkdown prints the command catalog as a markdown table. It reads
// only commands(), the same sorted and runner-enriched view used by `beacon
// list`, so the table has one source of truth and no second command registry.
func writeCommandMarkdown(out io.Writer) {
	fmt.Fprintln(out, "| Команда | Что делает | Использование |")
	fmt.Fprintln(out, "| --- | --- | --- |")
	for _, c := range commands() {
		fmt.Fprintf(out, "| `%s` | %s | `%s` |\n",
			escapeMarkdownCell(c.Name),
			escapeMarkdownCell(c.Summary),
			escapeMarkdownCell(c.Usage))
	}
}
