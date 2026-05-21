package tray

import (
	"log"
	"sync"

	"github.com/godbus/dbus/v5"
)

// DesktopNotifier sends notifications via org.freedesktop.Notifications.
// Cheap to construct; reuses a single session-bus connection across calls.
type DesktopNotifier struct {
	mu   sync.Mutex
	conn *dbus.Conn
}

// NewDesktopNotifier creates a notifier. Lazy-connects on first Notify.
func NewDesktopNotifier() *DesktopNotifier { return &DesktopNotifier{} }

// Notify posts a transient notification. Failures are logged, not returned —
// notifications are advisory only and shouldn't break the daemon.
func (n *DesktopNotifier) Notify(title, body string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conn == nil {
		c, err := dbus.SessionBus()
		if err != nil {
			log.Printf("notify: connect session bus: %v", err)
			return
		}
		n.conn = c
	}
	obj := n.conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	call := obj.Call(
		"org.freedesktop.Notifications.Notify", 0,
		"claude-watch", // app_name
		uint32(0),       // replaces_id
		"",              // app_icon
		title,           // summary
		body,            // body
		[]string{},      // actions
		map[string]dbus.Variant{},
		int32(5000), // timeout ms
	)
	if call.Err != nil {
		log.Printf("notify: %v", call.Err)
	}
}
