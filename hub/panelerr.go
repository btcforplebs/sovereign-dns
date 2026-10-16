package main

// Failed adds, in plain words. The control panel's errors are written for
// developers ("failed to parse npub: invalid checksum", "dial tcp …: connect:
// connection refused"), and a person adding a device needs to know what went
// wrong and what to do. plainPanelError says that; the log keeps the panel's
// own words next to it, so nothing is lost for whoever debugs it.

import (
	"context"
	"errors"
	"log"
	"net"
	"strings"
)

// plainPanelError turns an error from a panel write into one sentence or
// two for the page. Messages the panel already writes for people are kept.
func plainPanelError(err error) string {
	if err == nil {
		return ""
	}
	raw := err.Error()
	lower := strings.ToLower(raw)
	var netErr net.Error
	switch {
	case errors.Is(err, errBoxNotBackup):
		return "The box was added, but the control panel didn't make it a backup DNS box, so it won't answer while this box is off. Use Make backup DNS box on it in the control panel, or update Sovereign-DNS on this box and re-add it."
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return "The VPN control panel took too long to answer. It may still finish: reload this page in a minute and check the device list before trying again."
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "connection reset") || strings.Contains(lower, "eof"):
		return "The VPN control panel isn't answering. Check that Sovereign-DNS is running on this box, then try again."
	case strings.Contains(lower, "mock mode"):
		return "Nothing was sent: the hub is in demo mode (HUB_MOCK)."
	case strings.Contains(lower, "has no") && strings.Contains(lower, "route"),
		strings.Contains(lower, "can't import join requests"):
		return "This box's VPN control panel is too old for this. Update Sovereign-DNS on this box (run the install command again), then try again."
	case strings.Contains(lower, "only a network admin") || strings.Contains(lower, "not administered by this device"):
		return "This box isn't the network's admin, so it can't add devices. Add them on the box that runs the network."
	case strings.Contains(lower, "no dns box yet"):
		return "This network has no DNS box yet, so a box can't be added as its backup. Turn network DNS on first, then add the box."
	case strings.Contains(lower, "already the network's dns box"):
		return "That box is already the network's DNS box."
	case strings.Contains(lower, "must be a member"):
		return "The control panel says that device isn't on this network. Reload the page and try again."
	case strings.Contains(lower, "npub") || strings.Contains(lower, "bech32") || strings.Contains(lower, "pubkey") ||
		strings.Contains(lower, "checksum"):
		return "The control panel didn't accept that device ID. Copy it again from the device, or scan its QR code."
	case strings.Contains(lower, "join request") && (strings.Contains(lower, "expired") || strings.Contains(lower, "stale")):
		return "That join link has expired. Make a new one on the device and try again."
	case strings.Contains(lower, "accepted") && strings.Contains(lower, "doesn't list"):
		return "The control panel said yes but the device isn't on the list afterwards. Reload the page: if it is still missing, add it again."
	}
	return "The VPN control panel refused: " + raw
}

// logAddFailure writes what the reader saw and what the panel said, on one
// line, so a log reader can match one to the other.
func logAddFailure(what, who string, err error) {
	log.Printf("hub: adding %s %s failed: %s (panel: %v)", what, who, plainPanelError(err), err)
}
