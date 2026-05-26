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
		labelMode   string
		paceHot     bool
	}{
		{"low", 12, 6, false, true, "5h", false},
		{"mid", 42, 18, false, true, "5h", false},
		{"high", 73, 55, false, true, "5h", false},
		{"red", 96, 88, false, true, "5h", false},
		{"max", 100, 100, false, true, "5h", false},
		{"single", 5, 0, false, true, "5h", false},
		{"stale", 42, 18, true, true, "5h", false},
		{"empty", 0, 0, false, true, "5h", false},
		{"nodata", 0, 0, false, false, "5h", false},
		{"both-low", 12, 6, false, true, "both", false},
		{"both-mid", 42, 88, false, true, "both", false},
		{"both-high", 73, 55, false, true, "both", false},
		{"both-red", 96, 30, false, true, "both", false},
		{"both-nodata", 0, 0, false, false, "both", false},
		// Pace-hot variants: corner triangle in 5h-only and both modes.
		{"hot-low", 32, 18, false, true, "5h", true},
		{"hot-mid", 58, 22, false, true, "5h", true},
		{"hot-high", 78, 45, false, true, "5h", true},
		{"both-hot", 58, 45, false, true, "both", true},
	}
	for _, c := range cases {
		b, err := tray.RenderBars(c.five, c.seven, c.stale, c.hasAPI, c.labelMode, c.paceHot)
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
