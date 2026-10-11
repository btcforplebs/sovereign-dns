package main

import "errors"

// mock is one canned state of the stack, used when HUB_MOCK is set. It exists
// so the wizard can be rendered (and screenshotted) at every step without a
// running stack, and so copy changes can be checked against each state. The
// states are the ones a newcomer actually passes through, in order.
type mock struct {
	tick                                 tick
	tickErr                              error
	panelUp, piholeUp, docsUp, unboundUp bool
}

const mockNpub = "npub176fy8k3q2t0m9h7z4w5r6e8d0c2v4b6n8m0k2j4h6g8f0d2s4a6p8o0i2u4y6"

func mockState(name string) mock {
	base := tick{
		DaemonRunning: true,
		OwnNpub:       mockNpub,
		NetworkID:     "9683dbda",
		TunnelIP:      "10.44.5.1/16",
	}
	base.Networks = append(base.Networks, struct {
		Name                string `json:"name"`
		NetworkID           string `json:"networkId"`
		DNSOverride         bool   `json:"dnsOverride"`
		LocalIsDNSNode      bool   `json:"localIsDnsNode"`
		InboundJoinRequests []struct {
			Npub string `json:"npub"`
		} `json:"inboundJoinRequests"`
		Participants []struct {
			Alias              string `json:"alias"`
			Npub               string `json:"npub"`
			TunnelIP           string `json:"tunnelIp"`
			Reachable          bool   `json:"reachable"`
			State              string `json:"state"`
			DirectProbePending bool   `json:"fipsDirectProbePending"`
			TransportType      string `json:"fipsTransportType"`
			StatusText         string `json:"statusText"`
		} `json:"participants"`
	}{Name: "home", NetworkID: "9683dbda", DNSOverride: true, LocalIsDNSNode: true})
	net := &base.Networks[0]

	addDevice := func(alias, ip string, reachable bool, state, status string) {
		net.Participants = append(net.Participants, struct {
			Alias              string `json:"alias"`
			Npub               string `json:"npub"`
			TunnelIP           string `json:"tunnelIp"`
			Reachable          bool   `json:"reachable"`
			State              string `json:"state"`
			DirectProbePending bool   `json:"fipsDirectProbePending"`
			TransportType      string `json:"fipsTransportType"`
			StatusText         string `json:"statusText"`
		}{Alias: alias, Npub: "npub1" + alias + "0000000000000000000000000000000000000000000000000000000",
			TunnelIP: ip, Reachable: reachable, State: state, StatusText: status})
	}

	up := mock{tick: base, panelUp: true, piholeUp: true, docsUp: true, unboundUp: true}

	switch name {
	case "fresh":
		// Seconds after install.sh finished: the first build is still compiling.
		return mock{tickErr: errors.New("dial tcp: connect: connection refused")}
	case "starting":
		// The daemon is up, Pi-hole is not there yet.
		m := up
		m.piholeUp = false
		return m
	case "running":
		// Everything up, nobody has joined.
		return up
	case "pending":
		// A device ran the join command and is waiting for approval.
		m := up
		m.tick.Networks[0].InboundJoinRequests = append(m.tick.Networks[0].InboundJoinRequests,
			struct {
				Npub string `json:"npub"`
			}{Npub: "npub1phone000000000000000000000000000000000000000000000000000000"})
		return m
	case "approved":
		// Approved, but no traffic flows yet (VPN not connected on the device).
		m := up
		addDevice("laptop", "10.44.5.138/16", false, "offline", "approved, never connected")
		return m
	case "covered":
		m := up
		addDevice("laptop", "10.44.5.138/16", true, "online", "connected")
		return m
	default:
		return up
	}
}
