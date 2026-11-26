// Command beacon-tui monitors Beacon runs over the loopback API.
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

// main parses flags, validates the endpoint and runs the monitor.
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
		_ = forwardEvents(ctx, client, program)
	}()

	if _, err := program.Run(); err != nil {
		stop()
		fmt.Fprintln(os.Stderr, "beacon-tui:", err)
		os.Exit(1)
	}
	stop()
}

// msgSender is the part of a Bubble Tea program that the stream needs.
type msgSender interface {
	Send(tea.Msg)
}

// forwardEvents streams events from c and forwards each decoded event into p
// as a refreshMsg. Events that arrive after ctx is cancelled are dropped.
func forwardEvents(ctx context.Context, c *Client, p msgSender) error {
	return c.Stream(ctx, func(ev Event) {
		select {
		case <-ctx.Done():
		default:
			p.Send(refreshMsg{event: ev})
		}
	})
}
