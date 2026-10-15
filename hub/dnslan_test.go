package main

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"
)

// question is "example.com. A IN".
var question = []byte{7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}

// dnsMsg builds a message: header with id 0xBEEF and the given flags, one
// question, and the given additional records already encoded.
func dnsMsg(flags uint16, additional ...[]byte) []byte {
	m := binary.BigEndian.AppendUint16(nil, 0xBEEF)
	m = binary.BigEndian.AppendUint16(m, flags)
	m = binary.BigEndian.AppendUint16(m, 1)
	m = binary.BigEndian.AppendUint16(m, 0)
	m = binary.BigEndian.AppendUint16(m, 0)
	m = binary.BigEndian.AppendUint16(m, uint16(len(additional)))
	m = append(m, question...)
	for _, rr := range additional {
		m = append(m, rr...)
	}
	return m
}

// optRecord is an OPT record with the given UDP size and options.
func optRecord(size uint16, options ...[]byte) []byte {
	var rdata []byte
	for _, o := range options {
		rdata = append(rdata, o...)
	}
	r := []byte{0}
	r = binary.BigEndian.AppendUint16(r, optTypeOPT)
	r = binary.BigEndian.AppendUint16(r, size)
	r = append(r, 0, 0, 0x80, 0) // extended rcode, version, DO bit
	r = binary.BigEndian.AppendUint16(r, uint16(len(rdata)))
	return append(r, rdata...)
}

func option(code uint16, data []byte) []byte {
	o := binary.BigEndian.AppendUint16(nil, code)
	o = binary.BigEndian.AppendUint16(o, uint16(len(data)))
	return append(o, data...)
}

var cookie = option(10, []byte{1, 2, 3, 4, 5, 6, 7, 8})

// ecsOf returns the ECS address a message carries, its OPT's UDP size, and
// its other options. Every message here keeps its OPT last.
func ecsOf(t *testing.T, msg []byte) (addr netip.Addr, size uint16, hasOPT bool, others []byte) {
	t.Helper()
	rest, _, opt, err := splitOPT(msg)
	if err != nil {
		t.Fatalf("splitOPT: %v", err)
	}
	if opt == nil {
		return netip.Addr{}, 0, false, nil
	}
	if len(msg) < len(rest)+11 {
		t.Fatalf("OPT is not the last record: % x", msg)
	}
	rdata := msg[len(rest)+11:]
	for len(rdata) >= 4 {
		code := binary.BigEndian.Uint16(rdata)
		n := int(binary.BigEndian.Uint16(rdata[2:]))
		if code == ednsECSCode {
			a, ok := netip.AddrFromSlice(rdata[8 : 4+n])
			if !ok {
				t.Fatalf("bad ECS address % x", rdata[:4+n])
			}
			addr = a
		}
		rdata = rdata[4+n:]
	}
	return addr, opt.udpSize, true, opt.options
}

func TestClientSubnetAddedWhenTheDeviceSentNoOPT(t *testing.T) {
	client := netip.MustParseAddr("192.168.4.30")
	q, had, err := withClientSubnet(dnsMsg(0x0100), client)
	if err != nil {
		t.Fatal(err)
	}
	if had {
		t.Error("reported an OPT the query never had")
	}
	got, size, hasOPT, _ := ecsOf(t, q)
	if !hasOPT || got != client {
		t.Fatalf("ECS = %v (OPT %t), want %v", got, hasOPT, client)
	}
	if size != 512 {
		t.Errorf("offered %d bytes for a device that takes 512", size)
	}
	if !bytes.Equal(q[12:12+len(question)], question) || binary.BigEndian.Uint16(q[10:]) != 1 {
		t.Errorf("question or ARCOUNT changed: % x", q)
	}
}

func TestClientSubnetReplacesTheDevicesOwn(t *testing.T) {
	client := netip.MustParseAddr("192.168.4.30")
	spoof := option(ednsECSCode, []byte{0, 1, 32, 0, 8, 8, 8, 8})
	q, had, err := withClientSubnet(dnsMsg(0x0100, optRecord(1400, spoof, cookie)), client)
	if err != nil {
		t.Fatal(err)
	}
	got, size, _, others := ecsOf(t, q)
	if !had || got != client {
		t.Fatalf("ECS = %v, want %v (the device's own 8.8.8.8 must go)", got, client)
	}
	if bytes.Contains(q, []byte{8, 8, 8, 8}) {
		t.Error("the device's own ECS is still in the query")
	}
	if size != 1400 || !bytes.Equal(others, cookie) {
		t.Errorf("size %d, other options % x: want 1400 and the cookie kept", size, others)
	}
	if binary.BigEndian.Uint16(q[10:]) != 1 {
		t.Errorf("ARCOUNT = %d, want 1", binary.BigEndian.Uint16(q[10:]))
	}
}

func TestClientSubnetIPv6(t *testing.T) {
	client := netip.MustParseAddr("fd00::42")
	q, _, err := withClientSubnet(dnsMsg(0x0100), client)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _, _ := ecsOf(t, q); got != client {
		t.Fatalf("ECS = %v, want %v", got, client)
	}
}

func TestQueriesTheForwarderRefuses(t *testing.T) {
	tsig := append([]byte{0}, binary.BigEndian.AppendUint16(nil, 250)...)
	tsig = append(tsig, 0, 255, 0, 0, 0, 0, 0, 0)
	cases := map[string][]byte{
		"a response":    dnsMsg(0x8180),
		"two OPTs":      dnsMsg(0x0100, optRecord(1232), optRecord(1232)),
		"TSIG":          dnsMsg(0x0100, tsig),
		"short header":  {0xBE, 0xEF, 1},
		"cut question":  dnsMsg(0x0100)[:20],
		"trailing junk": append(dnsMsg(0x0100), 0xFF),
		"cut option":    dnsMsg(0x0100, append(optRecord(1232)[:9], 0, 3, 0, 8, 0)),
	}
	for name, q := range cases {
		if _, _, err := withClientSubnet(q, netip.MustParseAddr("10.0.0.2")); err == nil {
			t.Errorf("%s: passed on, want refused", name)
		}
	}
}

func TestAnswerLosesTheAddedSubnet(t *testing.T) {
	ours := ecsOption(netip.MustParseAddr("192.168.4.30"))
	resp := dnsMsg(0x8180, optRecord(1232, cookie, ours))

	plain, err := forClient(resp, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, hasOPT, _ := ecsOf(t, plain); hasOPT || binary.BigEndian.Uint16(plain[10:]) != 0 {
		t.Errorf("a device that sent no OPT got one back: % x", plain)
	}

	edns, err := forClient(resp, true)
	if err != nil {
		t.Fatal(err)
	}
	got, _, hasOPT, others := ecsOf(t, edns)
	if !hasOPT || got.IsValid() || !bytes.Equal(others, cookie) {
		t.Errorf("want the OPT back with its cookie and no ECS, got ECS %v, others % x", got, others)
	}
}

func TestOnlyHomeNetworkSendersAreServed(t *testing.T) {
	for _, s := range []string{"192.168.4.30", "10.0.0.8", "172.16.1.1", "127.0.0.1", "fe80::1", "fd00::1", "::ffff:192.168.4.30"} {
		if !lanClientAllowed(netip.MustParseAddr(s)) {
			t.Errorf("%s refused, want served", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "100.64.0.1", "2001:4860::8888", "::ffff:8.8.8.8", "1.1.1.1"} {
		if lanClientAllowed(netip.MustParseAddr(s)) {
			t.Errorf("%s served, want refused", s)
		}
	}
}

// fakePihole answers every UDP and TCP query by echoing it back as a response
// and records the ECS address each query carried.
func fakePihole(t *testing.T) (addr string, seen chan netip.Addr) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close(); l.Close() })
	seen = make(chan netip.Addr, 8)
	answer := func(q []byte) []byte {
		a, _, _, _ := ecsOf(t, q)
		seen <- a
		r := append([]byte(nil), q...)
		r[2] |= 0x80
		return r
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(answer(buf[:n]), from)
		}
	}()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				for {
					q, err := readTCPMessage(c)
					if err != nil {
						return
					}
					if writeTCPMessage(c, answer(q)) != nil {
						return
					}
				}
			}()
		}
	}()
	return pc.LocalAddr().String(), seen
}

func startForwarder(t *testing.T, upstream string, allowed func(netip.Addr) bool) string {
	t.Helper()
	f := dnsForwarder{upstream: upstream, allowed: allowed}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close(); l.Close() })
	go f.serveUDP(pc)
	go f.serveTCP(l)
	return pc.LocalAddr().String()
}

func TestForwarderEndToEnd(t *testing.T) {
	up, seen := fakePihole(t)
	fwd := startForwarder(t, up, lanClientAllowed)
	loop := netip.MustParseAddr("127.0.0.1")

	c, err := net.Dial("udp", fwd)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(dnsMsg(0x0100)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("UDP: no answer: %v", err)
	}
	if got := <-seen; got != loop {
		t.Errorf("UDP: Pi-hole saw client %v, want %v", got, loop)
	}
	if _, _, hasOPT, _ := ecsOf(t, buf[:n]); hasOPT {
		t.Error("UDP: the answer carries an OPT the device never sent")
	}

	tc, err := net.Dial("tcp", fwd)
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	_ = tc.SetDeadline(time.Now().Add(3 * time.Second))
	for i := 0; i < 2; i++ { // two queries on one connection
		if err := writeTCPMessage(tc, dnsMsg(0x0100, optRecord(1232, cookie))); err != nil {
			t.Fatal(err)
		}
		resp, err := readTCPMessage(tc)
		if err != nil {
			t.Fatalf("TCP query %d: no answer: %v", i+1, err)
		}
		if got := <-seen; got != loop {
			t.Errorf("TCP: Pi-hole saw client %v, want %v", got, loop)
		}
		if ecs, _, hasOPT, others := ecsOf(t, resp); !hasOPT || ecs.IsValid() || !bytes.Equal(others, cookie) {
			t.Errorf("TCP: answer OPT %t, ECS %v, others % x: want the device's OPT back without ours", hasOPT, ecs, others)
		}
	}
}

func TestForwarderIgnoresOutsiders(t *testing.T) {
	up, seen := fakePihole(t)
	fwd := startForwarder(t, up, func(netip.Addr) bool { return false })
	c, err := net.Dial("udp", fwd)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(500 * time.Millisecond))
	_, _ = c.Write(dnsMsg(0x0100))
	if _, err := c.Read(make([]byte, 512)); err == nil {
		t.Error("an outside sender got an answer")
	}
	select {
	case a := <-seen:
		t.Errorf("an outside sender's query reached Pi-hole (as %v)", a)
	default:
	}
}
