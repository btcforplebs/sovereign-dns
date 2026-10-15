package main

// DNS for the home network: lets a router hand this box out as the DNS server
// for every device in the house, with no app on any of them.
//
// Pi-hole lives in nvpn's namespace and answers on the mesh tunnel, which
// nothing on the home network can reach, and a port the host publishes cannot
// land in a namespace another container owns. So this forwarder runs as its
// own service (dns-lan in docker-compose.yml, off unless scripts/dns-lan.sh
// turns it on), takes the queries on the box's home-network address, and hands
// each one to Pi-hole at nvpn's dns-internal address (172.28.53.10). Pi-hole
// listens there only because of FTLCONF_misc_dnsmasq_lines "interface=eth0" in
// docker-compose.yml; its own interface setting binds the mesh tunnel and
// loopback alone. Pi-hole still decides everything: what is blocked,
// what Unbound is asked, what is logged.
//
// Two things a plain port forward would get wrong:
//   - Every query would reach Pi-hole from this forwarder's address, so the
//     query log would show one client and Pi-hole's per-client rate limit
//     (5000 a minute) would apply to the whole house at once. Each query
//     therefore carries the asking device's address in EDNS Client Subnet
//     (RFC 7871), which Pi-hole reads as the client (dns.EDNS0ECS, on by
//     default). Any ECS the device sent itself is removed first, so no device
//     can pose as another.
//   - The box would answer anyone who reaches it. Only private, loopback and
//     link-local senders are served, so a port forward on the router does not
//     turn the box into an open resolver for the internet.
//
// A query it cannot parse is dropped rather than passed on unchanged: passed
// on, its own ECS (if any) would reach Pi-hole untouched. The answer goes back
// with the ECS this forwarder added taken out again, so a device sees what it
// would have seen asking Pi-hole directly.

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	dnsLANListen  = ":5353"
	dnsLANTimeout = 5 * time.Second
	dnsMaxUDP     = 4096
	optTypeOPT    = 41
	ednsECSCode   = 8
)

var errBadMessage = errors.New("not a DNS query this forwarder can read")

// lanClientAllowed: who the forwarder answers. The same rule as the hub's
// home-network check (privateClient in auth.go).
func lanClientAllowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// skipName walks past one possibly-compressed domain name starting at off.
func skipName(msg []byte, off int) (int, error) {
	for {
		if off >= len(msg) {
			return 0, errBadMessage
		}
		l := int(msg[off])
		switch {
		case l == 0:
			return off + 1, nil
		case l&0xC0 == 0xC0:
			// A pointer ends the name in this place; where it points is not
			// our business, only that it is two bytes long.
			if off+2 > len(msg) {
				return 0, errBadMessage
			}
			return off + 2, nil
		case l&0xC0 != 0:
			return 0, errBadMessage
		default:
			off += 1 + l
		}
	}
}

// optRR is a message's EDNS OPT record, taken apart.
type optRR struct {
	udpSize, ttlHi, ttlLo uint16
	options               []byte // every option except ECS, in order, still encoded
	hadECS                bool
}

// splitOPT copies msg without its OPT record and returns that record apart,
// or nil when there is none. Everything else, in order, stays byte for byte.
// A message carrying a TSIG or SIG(0) record is refused: those sign the
// message as sent, must stay last, and are not something a home device sends
// to its resolver.
func splitOPT(msg []byte) (rest []byte, arcount int, opt *optRR, err error) {
	if len(msg) < 12 {
		return nil, 0, nil, errBadMessage
	}
	counts := [4]int{}
	for i := range counts {
		counts[i] = int(binary.BigEndian.Uint16(msg[4+2*i:]))
	}
	off := 12
	for i := 0; i < counts[0]; i++ {
		if off, err = skipName(msg, off); err != nil {
			return nil, 0, nil, err
		}
		if off += 4; off > len(msg) {
			return nil, 0, nil, errBadMessage
		}
	}
	for i := 0; i < counts[1]+counts[2]; i++ {
		if off, err = skipRR(msg, off); err != nil {
			return nil, 0, nil, err
		}
	}
	rest = make([]byte, 0, len(msg)+32)
	rest = append(rest, msg[:off]...)
	for i := 0; i < counts[3]; i++ {
		start := off
		nameEnd, err := skipName(msg, off)
		if err != nil {
			return nil, 0, nil, err
		}
		if off, err = skipRR(msg, start); err != nil {
			return nil, 0, nil, err
		}
		switch binary.BigEndian.Uint16(msg[nameEnd:]) {
		case 250, 24: // TSIG, SIG(0)
			return nil, 0, nil, errBadMessage
		case optTypeOPT:
		default:
			rest = append(rest, msg[start:off]...)
			arcount++
			continue
		}
		if opt != nil || nameEnd != start+1 { // two OPTs, or an OPT not at the root (RFC 6891 6.1.1)
			return nil, 0, nil, errBadMessage
		}
		opt = &optRR{
			udpSize: binary.BigEndian.Uint16(msg[nameEnd+2:]),
			ttlHi:   binary.BigEndian.Uint16(msg[nameEnd+4:]),
			ttlLo:   binary.BigEndian.Uint16(msg[nameEnd+6:]),
		}
		rdata := msg[nameEnd+10 : off]
		for len(rdata) > 0 {
			if len(rdata) < 4 {
				return nil, 0, nil, errBadMessage
			}
			n := int(binary.BigEndian.Uint16(rdata[2:]))
			if 4+n > len(rdata) {
				return nil, 0, nil, errBadMessage
			}
			if binary.BigEndian.Uint16(rdata) == ednsECSCode {
				opt.hadECS = true
			} else {
				opt.options = append(opt.options, rdata[:4+n]...)
			}
			rdata = rdata[4+n:]
		}
	}
	if off != len(msg) {
		return nil, 0, nil, errBadMessage // trailing bytes
	}
	binary.BigEndian.PutUint16(rest[10:], uint16(arcount))
	return rest, arcount, opt, nil
}

// appendOPT adds opt (with extra options after its own) as the last record.
func appendOPT(rest []byte, arcount int, opt optRR, extra []byte) []byte {
	opts := append(append([]byte(nil), opt.options...), extra...)
	out := append(rest, 0) // root name
	out = binary.BigEndian.AppendUint16(out, optTypeOPT)
	out = binary.BigEndian.AppendUint16(out, opt.udpSize)
	out = binary.BigEndian.AppendUint16(out, opt.ttlHi)
	out = binary.BigEndian.AppendUint16(out, opt.ttlLo)
	out = binary.BigEndian.AppendUint16(out, uint16(len(opts)))
	out = append(out, opts...)
	binary.BigEndian.PutUint16(out[10:], uint16(arcount+1))
	return out
}

// withClientSubnet returns query with its EDNS Client Subnet set to exactly
// client (a /32 or /128), replacing any ECS it carried. It also reports
// whether the query had an OPT record of its own: a query without one gets
// one, offering 512 bytes, the size a device that sent no OPT can take.
func withClientSubnet(query []byte, client netip.Addr) ([]byte, bool, error) {
	if len(query) < 12 || query[2]&0x80 != 0 { // QR set: a response, not a query
		return nil, false, errBadMessage
	}
	rest, ar, opt, err := splitOPT(query)
	if err != nil {
		return nil, false, err
	}
	if ar >= 0xFFFF {
		return nil, false, errBadMessage
	}
	had := opt != nil
	if !had {
		opt = &optRR{udpSize: 512}
	}
	return appendOPT(rest, ar, *opt, ecsOption(client)), had, nil
}

// forClient undoes withClientSubnet on Pi-hole's answer: the ECS this
// forwarder added comes out (the device never sent it, or sent another one
// and would reject an answer naming ours), and so does the whole OPT record
// when the device sent none, since an answer must not carry one then.
func forClient(resp []byte, clientHadOPT bool) ([]byte, error) {
	rest, ar, opt, err := splitOPT(resp)
	if err != nil {
		return nil, err
	}
	if opt == nil || !clientHadOPT {
		return rest, nil
	}
	return appendOPT(rest, ar, *opt, nil), nil
}

// skipRR walks past one resource record.
func skipRR(msg []byte, off int) (int, error) {
	off, err := skipName(msg, off)
	if err != nil {
		return 0, err
	}
	if off+10 > len(msg) {
		return 0, errBadMessage
	}
	n := int(binary.BigEndian.Uint16(msg[off+8:]))
	off += 10 + n
	if off > len(msg) {
		return 0, errBadMessage
	}
	return off, nil
}

// ecsOption is an EDNS Client Subnet option naming exactly one address.
func ecsOption(client netip.Addr) []byte {
	client = client.Unmap()
	family, bits := uint16(1), 32
	if client.Is6() {
		family, bits = 2, 128
	}
	addr := client.AsSlice()
	o := binary.BigEndian.AppendUint16(nil, ednsECSCode)
	o = binary.BigEndian.AppendUint16(o, uint16(4+len(addr)))
	o = binary.BigEndian.AppendUint16(o, family)
	o = append(o, byte(bits), 0) // source prefix, scope prefix
	return append(o, addr...)
}

type dnsForwarder struct {
	upstream string // Pi-hole, host:port
	allowed  func(netip.Addr) bool
}

// exchangeUDP sends one query to Pi-hole and returns its answer.
func (f dnsForwarder) exchangeUDP(ctx context.Context, q []byte) ([]byte, error) {
	d := net.Dialer{Timeout: dnsLANTimeout}
	c, err := d.DialContext(ctx, "udp", f.upstream)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(dnsLANTimeout))
	if _, err := c.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, dnsMaxUDP)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return nil, err
		}
		// Only the answer to this query: same ID.
		if n >= 2 && buf[0] == q[0] && buf[1] == q[1] {
			return buf[:n], nil
		}
	}
}

func (f dnsForwarder) serveUDP(pc net.PacketConn) error {
	buf := make([]byte, dnsMaxUDP)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		ap, ok := addrOf(from)
		if !ok || !f.allowed(ap) {
			continue
		}
		q, hadOPT, err := withClientSubnet(buf[:n], ap)
		if err != nil {
			continue
		}
		go func() {
			resp, err := f.exchangeUDP(context.Background(), q)
			if err == nil {
				resp, err = forClient(resp, hadOPT)
			}
			if err != nil {
				log.Printf("dns-lan: %v", err)
				return
			}
			_, _ = pc.WriteTo(resp, from)
		}()
	}
}

func (f dnsForwarder) serveTCP(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go f.handleTCP(c)
	}
}

// handleTCP answers length-prefixed queries on one connection, one at a time,
// over one connection to Pi-hole.
func (f dnsForwarder) handleTCP(c net.Conn) {
	defer c.Close()
	ap, ok := addrOf(c.RemoteAddr())
	if !ok || !f.allowed(ap) {
		return
	}
	var up net.Conn
	defer func() {
		if up != nil {
			up.Close()
		}
	}()
	for {
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		q, err := readTCPMessage(c)
		if err != nil {
			return
		}
		q, hadOPT, err := withClientSubnet(q, ap)
		if err != nil {
			return
		}
		if up == nil {
			d := net.Dialer{Timeout: dnsLANTimeout}
			if up, err = d.Dial("tcp", f.upstream); err != nil {
				log.Printf("dns-lan: %v", err)
				return
			}
		}
		_ = up.SetDeadline(time.Now().Add(dnsLANTimeout))
		if err := writeTCPMessage(up, q); err != nil {
			return
		}
		resp, err := readTCPMessage(up)
		if err == nil {
			resp, err = forClient(resp, hadOPT)
		}
		if err != nil {
			return
		}
		if err := writeTCPMessage(c, resp); err != nil {
			return
		}
	}
}

func readTCPMessage(r io.Reader) ([]byte, error) {
	var l [2]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	m := make([]byte, binary.BigEndian.Uint16(l[:]))
	_, err := io.ReadFull(r, m)
	return m, err
}

func writeTCPMessage(w io.Writer, m []byte) error {
	if len(m) > 0xFFFF {
		return errBadMessage
	}
	_, err := w.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(m))), m...))
	return err
}

func addrOf(a net.Addr) (netip.Addr, bool) {
	switch v := a.(type) {
	case *net.UDPAddr:
		ip, ok := netip.AddrFromSlice(v.IP)
		return ip.Unmap(), ok
	case *net.TCPAddr:
		ip, ok := netip.AddrFromSlice(v.IP)
		return ip.Unmap(), ok
	}
	return netip.Addr{}, false
}

func runDNSLAN() {
	f := dnsForwarder{upstream: env("DNS_LAN_UPSTREAM", "172.28.53.10:53"), allowed: lanClientAllowed}
	listen := env("DNS_LAN_LISTEN", dnsLANListen)
	pc, err := net.ListenPacket("udp", listen)
	if err != nil {
		log.Fatalf("dns-lan: %v", err)
	}
	l, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatalf("dns-lan: %v", err)
	}
	log.Printf("dns-lan: answering home-network queries on %s through Pi-hole at %s", listen, f.upstream)
	var once sync.Once
	fatal := func(err error) { once.Do(func() { log.Fatalf("dns-lan: %v", err) }) }
	go func() { fatal(f.serveTCP(l)) }()
	fatal(f.serveUDP(pc))
}

// dnsLANHealth resolves a name through the forwarder itself, so it goes red
// when Pi-hole stops answering behind it and not only when this process dies.
func dnsLANHealth() int {
	if probeDNS(context.Background(), "127.0.0.1"+env("DNS_LAN_LISTEN", dnsLANListen)) {
		return 0
	}
	return 1
}
