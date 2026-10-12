package main

// A minimal DNS client: enough to ask for A/AAAA/TXT with the DO bit set and
// read back the rcode, the AD bit and the address records. Standard library
// only, so the auditor builds to a static binary like the hub.

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"
)

const (
	typeA     = 1
	typeCNAME = 5
	typeTXT   = 16
	typeAAAA  = 28

	rcodeNoError  = 0
	rcodeServFail = 2
	rcodeNXDomain = 3
)

// Answer is one witness's view of one (name, type) question.
type Answer struct {
	Rcode int          `json:"rcode"`
	AD    bool         `json:"ad"`
	Addrs []netip.Addr `json:"addrs,omitempty"`
	TXT   []string     `json:"-"`
}

// buildQuery returns a wire-format query with RD set and an EDNS OPT record
// carrying the DO bit, so a validating resolver reports AD for signed data.
// id 0 is what RFC 8484 asks of DoH clients; plain UDP callers set their own.
func buildQuery(name string, qtype uint16, id uint16) ([]byte, error) {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, id)
	b.Write([]byte{0x01, 0x20}) // RD + AD (ask for the AD bit, RFC 6840 5.7)
	b.Write([]byte{0, 1, 0, 0, 0, 0, 0, 1})
	for _, l := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if l == "" || len(l) > 63 {
			return nil, fmt.Errorf("bad name %q", name)
		}
		b.WriteByte(byte(len(l)))
		b.WriteString(l)
	}
	b.WriteByte(0)
	binary.Write(&b, binary.BigEndian, qtype)
	b.Write([]byte{0, 1})
	b.Write([]byte{0, 0, 41, 0x04, 0xd0, 0, 0, 0x80, 0, 0, 0}) // OPT: udp 1232, DO
	return b.Bytes(), nil
}

var errShort = errors.New("truncated DNS message")

func skipName(m []byte, o int) (int, error) {
	for i := 0; i < 128; i++ {
		if o >= len(m) {
			return 0, errShort
		}
		l := int(m[o])
		switch {
		case l == 0:
			return o + 1, nil
		case l&0xC0 == 0xC0:
			return o + 2, nil
		default:
			o += 1 + l
		}
	}
	return 0, errors.New("name too long")
}

// parseAnswer reads the header and the answer section. Only records of the
// asked type are collected; a CNAME chain is followed implicitly because a
// recursive answer carries the final A/AAAA records in the same section.
func parseAnswer(m []byte, qtype uint16) (Answer, bool, error) {
	if len(m) < 12 {
		return Answer{}, false, errShort
	}
	flags := binary.BigEndian.Uint16(m[2:])
	a := Answer{Rcode: int(flags & 0xF), AD: flags&0x20 != 0}
	tc := flags&0x200 != 0
	qd, an := int(binary.BigEndian.Uint16(m[4:])), int(binary.BigEndian.Uint16(m[6:]))
	o := 12
	var err error
	for i := 0; i < qd; i++ {
		if o, err = skipName(m, o); err != nil {
			return a, tc, err
		}
		o += 4
	}
	for i := 0; i < an; i++ {
		if o, err = skipName(m, o); err != nil {
			return a, tc, err
		}
		if o+10 > len(m) {
			return a, tc, errShort
		}
		t := binary.BigEndian.Uint16(m[o:])
		rdl := int(binary.BigEndian.Uint16(m[o+8:]))
		if o+10+rdl > len(m) {
			return a, tc, errShort
		}
		rd := m[o+10 : o+10+rdl]
		if t == qtype {
			switch t {
			case typeA, typeAAAA:
				if ip, ok := netip.AddrFromSlice(rd); ok {
					a.Addrs = append(a.Addrs, ip.Unmap())
				}
			case typeTXT:
				var s strings.Builder
				for j := 0; j < len(rd); {
					n := int(rd[j])
					if j+1+n > len(rd) {
						break
					}
					s.Write(rd[j+1 : j+1+n])
					j += 1 + n
				}
				a.TXT = append(a.TXT, s.String())
			}
		}
		o += 10 + rdl
	}
	return a, tc, nil
}

// Resolver asks a plain DNS server (the box's own Unbound: witness 1).
type Resolver struct {
	Addr    string // host:port
	Timeout time.Duration
}

// Query retries once: a cold recursive cache can take seconds, and one lost
// UDP packet must not turn into an inconclusive check.
func (r Resolver) Query(name string, qtype uint16) (Answer, error) {
	a, err := r.query(name, qtype)
	if err != nil {
		a, err = r.query(name, qtype)
	}
	return a, err
}

func (r Resolver) query(name string, qtype uint16) (Answer, error) {
	var idb [2]byte
	rand.Read(idb[:])
	id := binary.BigEndian.Uint16(idb[:])
	q, err := buildQuery(name, qtype, id)
	if err != nil {
		return Answer{}, err
	}
	resp, err := r.exchange("udp", q)
	if err != nil {
		return Answer{}, err
	}
	a, tc, err := parseAnswer(resp, qtype)
	if err == nil && tc {
		if resp, err = r.exchange("tcp", q); err != nil {
			return Answer{}, err
		}
		a, _, err = parseAnswer(resp, qtype)
	}
	if err == nil && binary.BigEndian.Uint16(resp) != id {
		err = errors.New("DNS id mismatch")
	}
	return a, err
}

func (r Resolver) exchange(network string, q []byte) ([]byte, error) {
	to := r.Timeout
	if to == 0 {
		to = 5 * time.Second
	}
	c, err := net.DialTimeout(network, r.Addr, to)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(to))
	if network == "tcp" {
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(q)))
		if _, err := c.Write(append(l[:], q...)); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return nil, err
		}
		buf := make([]byte, binary.BigEndian.Uint16(l[:]))
		_, err = io.ReadFull(c, buf)
		return buf, err
	}
	if _, err := c.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	n, err := c.Read(buf)
	return buf[:n], err
}
