//go:build !linux

package tray

import "context"

// EnsureAppindicator is a no-op outside Linux. macOS's tray uses the native
// NSStatusBar via fyne.io/systray; there's nothing to enable.
func EnsureAppindicator(_ context.Context) error { return nil }
