package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:7432", "loopback daemon endpoint")
	token := flag.String("token", os.Getenv("BEACON_API_TOKEN"), "bearer token (default BEACON_API_TOKEN)")
	flag.Parse()

	client := NewClient(*endpoint, *token)
	if _, err := client.endpoint(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	program := tea.NewProgram(newModel(client))
	go func() {
		_ = client.Stream(ctx, func(Event) {
			select {
			case <-ctx.Done():
			default:
				program.Send(refreshMsg{})
			}
		})
	}()

	if _, err := program.Run(); err != nil {
		stop()
		fmt.Fprintln(os.Stderr, "beacon-tui:", err)
		os.Exit(1)
	}
	stop()
}
