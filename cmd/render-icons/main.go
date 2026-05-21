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
	}{
		{"low", 12, 6, false, true, "5h"},
		{"mid", 42, 18, false, true, "5h"},
		{"high", 73, 55, false, true, "5h"},
		{"red", 96, 88, false, true, "5h"},
		{"max", 100, 100, false, true, "5h"},
		{"single", 5, 0, false, true, "5h"},
		{"stale", 42, 18, true, true, "5h"},
		{"empty", 0, 0, false, true, "5h"},
		{"nodata", 0, 0, false, false, "5h"},
		{"both-low", 12, 6, false, true, "both"},
		{"both-mid", 42, 88, false, true, "both"},
		{"both-high", 73, 55, false, true, "both"},
		{"both-red", 96, 30, false, true, "both"},
		{"both-nodata", 0, 0, false, false, "both"},
	}
	for _, c := range cases {
		b, err := tray.RenderBars(c.five, c.seven, c.stale, c.hasAPI, c.labelMode)
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
