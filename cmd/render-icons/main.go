package main

import (
	"fmt"
	"os"

	"github.com/jrlucier/claude-watch/internal/tray"
)

func main() {
	cases := []struct {
		name        string
		five, seven float64
		stale       bool
		hasAPI      bool
	}{
		{"low", 12, 6, false, true},
		{"mid", 42, 18, false, true},
		{"high", 73, 55, false, true},
		{"red", 96, 88, false, true},
		{"max", 100, 100, false, true},
		{"single", 5, 0, false, true},
		{"stale", 42, 18, true, true},
		{"empty", 0, 0, false, true},
		{"nodata", 0, 0, false, false},
	}
	for _, c := range cases {
		b, err := tray.RenderBars(c.five, c.seven, c.stale, c.hasAPI)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		path := fmt.Sprintf("/tmp/icon-%s.png", c.name)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(path, len(b), "bytes")
	}
}
