package main

// installBoardAdapter plugs the board command into the CLI dispatch through
// the seam left by IDX-10. The adapter lives in cmd/beacon and calls the local
// runner in board.go, so it adds no cmd to internal/tui import edge.
func installBoardAdapter() {
	RegisterBoard(func(c *CLI, args []string) int {
		return runBoard(c, args)
	})
}

func init() {
	installBoardAdapter()
}
