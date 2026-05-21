//go:build !linux

package tray

// DesktopNotifier is a no-op stub outside Linux. macOS has its own native
// path which we're not wiring up today — the daemon still works, just without
// threshold notifications.
type DesktopNotifier struct{}

func NewDesktopNotifier() *DesktopNotifier         { return &DesktopNotifier{} }
func (n *DesktopNotifier) Notify(string, string)    {}
