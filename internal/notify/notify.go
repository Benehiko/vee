// Package notify sends desktop notifications via the freedesktop
// org.freedesktop.Notifications D-Bus interface on the session bus.
//
// All operations are best-effort: if the session bus is not reachable
// (headless host, missing DBUS_SESSION_BUS_ADDRESS) Send returns nil so
// the daemon never fails on notification delivery.
package notify

import (
	"fmt"
	"time"

	dbus "github.com/godbus/dbus/v5"
)

// Urgency mirrors the FDO notification urgency hint values.
type Urgency byte

const (
	UrgencyLow      Urgency = 0
	UrgencyNormal   Urgency = 1
	UrgencyCritical Urgency = 2
)

const (
	notifyDest    = "org.freedesktop.Notifications"
	notifyPath    = "/org/freedesktop/Notifications"
	notifyMethod  = "org.freedesktop.Notifications.Notify"
	defaultAppID  = "vee"
	defaultIcon   = "computer"
	connectBudget = 2 * time.Second
)

// Send delivers a desktop notification. Returns nil on success or when the
// session bus is unreachable; returns the underlying error only if the bus
// connected but the Notify call itself failed.
func Send(summary, body string, urgency Urgency) error {
	type result struct {
		conn *dbus.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := dbus.ConnectSessionBus()
		ch <- result{c, err}
	}()

	var conn *dbus.Conn
	select {
	case r := <-ch:
		if r.err != nil {
			return nil
		}
		conn = r.conn
	case <-time.After(connectBudget):
		return nil
	}
	defer func() { _ = conn.Close() }()

	hints := map[string]dbus.Variant{
		"urgency": dbus.MakeVariant(byte(urgency)),
	}

	obj := conn.Object(notifyDest, notifyPath)
	call := obj.Call(notifyMethod, 0,
		defaultAppID, // app_name
		uint32(0),    // replaces_id
		defaultIcon,  // app_icon
		summary,      // summary
		body,         // body
		[]string{},   // actions
		hints,        // hints
		int32(-1),    // expire_timeout: server default
	)
	if call.Err != nil {
		return fmt.Errorf("notify call: %w", call.Err)
	}
	return nil
}
