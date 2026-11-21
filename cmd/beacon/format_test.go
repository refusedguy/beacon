package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseFormatFlagAcceptsTableAndJSON(t *testing.T) {
	for _, format := range []string{"table", "json"} {
		var errOut bytes.Buffer
		if !parseFormatFlag(format, "beacon test: неизвестный формат %q\n", &errOut, true) {
			t.Fatalf("формат %q отклонён: %q", format, errOut.String())
		}
		if errOut.Len() != 0 {
			t.Fatalf("формат %q записал в stderr: %q", format, errOut.String())
		}
	}
}

func TestParseFormatFlagKeepsCommandErrorAndUsage(t *testing.T) {
	const errFmt = "beacon pr create: неизвестный формат %q\n"
	const want = "beacon pr create: неизвестный формат \"yaml\"\n"
	for _, tc := range []struct {
		name      string
		showUsage bool
	}{
		{"без usage", false},
		{"с usage", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errOut bytes.Buffer
			if parseFormatFlag("yaml", errFmt, &errOut, tc.showUsage) {
				t.Fatal("неизвестный формат принят")
			}
			if !strings.HasPrefix(errOut.String(), want) {
				t.Fatalf("текст ошибки изменён: %q", errOut.String())
			}
			gotUsage := strings.Contains(errOut.String(), usageText)
			if gotUsage != tc.showUsage {
				t.Fatalf("usage показан = %v, ожидалось %v: %q", gotUsage, tc.showUsage, errOut.String())
			}
		})
	}
}
