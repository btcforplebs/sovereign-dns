package main

// Dashboard alerts: states the reader has to act on, said at the top of the
// page in plain words instead of being left for them to notice in a tile.
//
// Each one is a failure that has already happened on a live box:
//   - The VPN disconnected on the DNS box. The Disconnect button is in the
//     control panel, so the hub can't warn before it is pressed; it says so
//     the moment it sees it instead.
//   - The last backup DNS box left. With none, the network has no DNS while
//     the DNS box is off, asleep or updating.
//   - The control panel and the VPN daemon run different builds, as when one
//     container was swapped to a new image and the other was not.

import (
	"fmt"
	"sync"
)

type alert struct {
	Level  string // "down" or "warn", as the tiles
	Title  string
	Detail string
}

// backupMemory remembers that a network has had a backup DNS box, so that
// "none left" can be told from "never had one" (a single box is a normal
// setup and deserves no alert). It lives as long as the hub process: after a
// restart the alert comes back only once a backup has been seen again.
type backupMemory struct {
	sync.Mutex
	networks map[string]bool
}

// note records whether a network has backups now and reports whether it had
// any before. Keyed by the mesh network ID, so joining another network
// starts over. A nil memory remembers nothing.
func (m *backupMemory) note(networkID string, backups int) (hadBefore bool) {
	if m == nil {
		return false
	}
	m.Lock()
	defer m.Unlock()
	if m.networks == nil {
		m.networks = map[string]bool{}
	}
	hadBefore = m.networks[networkID]
	if backups > 0 {
		m.networks[networkID] = true
	}
	return hadBefore
}

func buildAlerts(p page, state tick, seen *backupMemory) []alert {
	var alerts []alert
	net := p.network
	if net != nil && state.DaemonRunning && !state.VPNEnabled && net.DNSOverride {
		switch {
		case net.LocalIsDNSNode:
			alerts = append(alerts, alert{
				Level: "down",
				Title: "The VPN is paused on this box, and it is the network's DNS box.",
				Detail: "Devices on the mesh can't look anything up through it until it reconnects. " +
					"Press Connect in the control panel." + backupNote(p.Backups),
			})
		case isBackupHere(net, state.OwnNpub):
			alerts = append(alerts, alert{
				Level:  "warn",
				Title:  "The VPN is paused on this box, so it can't stand in as a backup DNS box.",
				Detail: "Press Connect in the control panel.",
			})
		}
	}

	if net != nil && net.DNSNodeNpub != "" && seen.note(net.NetworkID, p.Backups) && p.Backups == 0 {
		alerts = append(alerts, alert{
			Level: "warn",
			Title: "No backup DNS box left.",
			Detail: "This network had one. Now, while the DNS box is off, asleep or updating, devices on the mesh can't look anything up. " +
				"Add a box back under Add another DNS box, or re-add it below if it was removed.",
		})
	}

	if a, d := state.AppVersion, state.DaemonVersion; a != "" && d != "" && a != d {
		alerts = append(alerts, alert{
			Level: "warn",
			Title: "The control panel and the VPN are running different builds.",
			Detail: fmt.Sprintf("Control panel %s, VPN %s. One of them was updated and the other wasn't. "+
				"Run the update again so both come from the same image.", a, d),
		})
	}
	return alerts
}

func backupNote(backups int) string {
	switch backups {
	case 0:
		return " There is no backup DNS box to answer meanwhile."
	case 1:
		return " The backup DNS box answers meanwhile, if it is online."
	}
	return " The backup DNS boxes answer meanwhile, if they are online."
}

func isBackupHere(net *tickNetwork, own string) bool {
	for _, participant := range net.Participants {
		if participant.ServesDNSBackup && (participant.State == "local" || (own != "" && participant.Npub == own)) {
			return true
		}
	}
	return false
}
