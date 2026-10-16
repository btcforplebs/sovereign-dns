package main

// Device setup in the terminal, right after install.sh starts the stack, so a
// box with no screen is set up without opening anything (`hub setup`, run by
// the installer through `docker compose exec hub /hub setup`).
//
// It asks the setup wizard's second question, "First box or another one?",
// and then does what the wizard's next step does, with the same checks and
// the same control-panel writes (joinFirstBox, checkAddBox and addBox in
// boxjoin.go, checkAddDevice and importJoinRequest in join.go):
//
//   First box: shows how a phone joins, and a QR code of this box's add-device
//     page when the dashboard is on the home network, so the whole thing can
//     be done on the phone. Then it reads join links (or another box's ID),
//     one per line, shows which device each names and adds it on "y". The
//     review is the approval, as on the dashboard. Enter on an empty line ends.
//   Another box: reads the first box's join code, joins, prints this box's ID
//     (and its QR code) to paste back there, and waits for that.
//
// Nothing here is new policy: every refusal is the dashboard's, word for word.
// With --if-new (what the installer passes) it stays quiet on a box that is
// already set up, so running the installer again to update asks nothing.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// setupIO is the terminal: lines in, text out. Tests drive it with a script.
type setupIO struct {
	in  *bufio.Reader
	out io.Writer
}

// line reads one line, trimmed. ok is false at the end of input (Ctrl-D, or
// no terminal at all), which every prompt treats as "skip".
func (t setupIO) line(prompt string) (string, bool) {
	fmt.Fprint(t.out, prompt)
	s, err := t.in.ReadString('\n')
	if err != nil && s == "" {
		fmt.Fprintln(t.out)
		return "", false
	}
	return strings.TrimSpace(s), true
}

func (t setupIO) yes(prompt string) bool {
	a, _ := t.line(prompt)
	a = strings.ToLower(a)
	return a == "y" || a == "yes"
}

type setupOptions struct {
	ifNew    bool          // the installer's run: say nothing more on a box already set up
	url      string        // this box's add-device page on the home network, "" when it has none
	ready    time.Duration // how long to wait for the VPN before giving up
	admitted time.Duration // how long a second box waits for the first to add it
	poll     time.Duration
}

func runSetup(args []string) int {
	opts := setupOptions{
		url:      strings.TrimSpace(os.Getenv("SETUP_URL")),
		ready:    90 * time.Second,
		admitted: 10 * time.Minute,
		poll:     3 * time.Second,
	}
	for _, a := range args {
		switch a {
		case "--if-new":
			opts.ifNew = true
		default:
			fmt.Fprintf(os.Stderr, "usage: hub setup [--if-new]\n")
			return 2
		}
	}
	cfg := loadConfig()
	t := setupIO{in: bufio.NewReader(os.Stdin), out: os.Stdout}
	if err := cfg.terminalSetup(context.Background(), t, opts); err != nil {
		fmt.Fprintln(os.Stdout, err)
		return 1
	}
	return 0
}

const dashboardLater = "You can do this any time on the dashboard, or run:  docker compose exec hub /hub setup"

func (c config) terminalSetup(ctx context.Context, t setupIO, o setupOptions) error {
	p, ok := c.waitReady(ctx, o)
	if !ok {
		fmt.Fprintln(t.out, "The VPN isn't ready yet, so device setup can't start here.")
		fmt.Fprintln(t.out, dashboardLater)
		return nil
	}

	// The facts beat the question, as in the wizard (boxRole).
	switch {
	case p.Member:
		return c.secondBoxWait(ctx, t, o, p)
	case len(p.Devices) > 0 || p.PendingJoins > 0:
		if o.ifNew {
			fmt.Fprintf(t.out, "Device setup: already done (%s on this box's network).\n", plural(len(p.Devices), "device"))
			fmt.Fprintln(t.out, "To add more: the dashboard, or  docker compose exec hub /hub setup")
			return nil
		}
		return c.firstBox(ctx, t, o, p)
	}

	fmt.Fprintln(t.out)
	fmt.Fprintln(t.out, "Set up your devices now?")
	fmt.Fprintln(t.out, "  1) This is my first box. Phones, laptops and any other DNS box join it.")
	fmt.Fprintln(t.out, "  2) I already have a box. This one joins it as a second DNS box.")
	for {
		a, ok := t.line("Type 1 or 2, or press Enter to skip: ")
		switch {
		case !ok || a == "":
			fmt.Fprintln(t.out, "Skipped. "+dashboardLater)
			return nil
		case a == "1":
			return c.firstBox(ctx, t, o, p)
		case a == "2":
			return c.secondBox(ctx, t, o)
		}
		fmt.Fprintln(t.out, "Please type 1 or 2.")
	}
}

// waitReady waits until the box can add or join: its VPN knows its own ID and
// network (AddReady), or it has already asked to join another box (Member).
func (c config) waitReady(ctx context.Context, o setupOptions) (page, bool) {
	deadline := time.Now().Add(o.ready)
	for {
		p := c.buildPage(ctx)
		if p.AddReady || p.Member {
			return p, true
		}
		if time.Now().After(deadline) {
			return p, false
		}
		time.Sleep(o.poll)
	}
}

func (c config) firstBox(ctx context.Context, t setupIO, o setupOptions, p page) error {
	fmt.Fprintln(t.out)
	fmt.Fprintln(t.out, "Add a phone or computer")
	fmt.Fprintln(t.out, "  On the phone: install Nostr VPN, open it, go to Join Network and tap Copy Request.")
	fmt.Fprintln(t.out, "  On a computer with Nostr VPN: run  nvpn join-request --no-qr  and copy the line.")
	if o.url != "" {
		fmt.Fprintln(t.out, "  Easiest from a phone on your home Wi-Fi: open this box's Add a device page")
		fmt.Fprintln(t.out, "  with the phone's camera, sign in with the dashboard password and paste it there.")
		c.printQR(ctx, t.out, o.url)
		fmt.Fprintln(t.out, "    "+o.url)
	}
	fmt.Fprintln(t.out, "  Or paste it below. Treat it like a one-time password: hand it over directly,")
	fmt.Fprintln(t.out, "  not through chat or email.")
	if p.JoinCode != "" {
		fmt.Fprintln(t.out)
		fmt.Fprintln(t.out, "Adding another DNS box? In its setup, choose 2 and paste this join code:")
		fmt.Fprintln(t.out, "    "+p.JoinCode)
		fmt.Fprintln(t.out, "  It then shows its own ID: paste that below.")
	}
	fmt.Fprintln(t.out)

	added := 0
	for {
		raw, ok := t.line("Paste a join link (or another box's ID), or press Enter to finish: ")
		if !ok || raw == "" {
			break
		}
		// Decide from fresh state each time, never from what was shown before.
		p = c.buildPage(ctx)
		var err error
		var did bool
		if strings.HasPrefix(strings.ToLower(raw), joinLinkPrefix) {
			did, err = c.termAddDevice(ctx, t, p, raw)
		} else if _, perr := parseBoxID(raw); perr == nil {
			did, err = c.termAddBox(ctx, t, p, raw)
		} else {
			_, _, err = parseJoinLink(raw)
		}
		if err != nil {
			fmt.Fprintln(t.out, "  "+err.Error())
			continue
		}
		if did {
			added++
		}
	}
	p = c.buildPage(ctx)
	fmt.Fprintf(t.out, "Device setup done: %s on this box's network", plural(len(p.Devices), "device"))
	if added > 0 {
		fmt.Fprintf(t.out, " (%d added just now)", added)
	}
	fmt.Fprintln(t.out, ".")
	return nil
}

func (c config) termAddDevice(ctx context.Context, t setupIO, p page, raw string) (bool, error) {
	link, j, err := parseJoinLink(raw)
	if err != nil {
		return false, err
	}
	already, err := checkAddDevice(p, j)
	if err != nil {
		return false, err
	}
	name := j.Label
	if name == "" {
		name = "a device"
	}
	if already {
		fmt.Fprintf(t.out, "  %s (%s) is already on this network.\n", name, j.DeviceNpub)
		return false, nil
	}
	fmt.Fprintf(t.out, "  This link is from %s, device ID %s.\n", name, j.DeviceNpub)
	fmt.Fprintln(t.out, "  Add it only if you recognise it (the app shows the ID under Join Network, Manual join).")
	if !t.yes(fmt.Sprintf("  Add it to %s? [y/N] ", networkLabel(p))) {
		fmt.Fprintln(t.out, "  Not added.")
		return false, nil
	}
	if err := c.importJoinRequest(context.WithoutCancel(ctx), link, j.DeviceNpub); err != nil {
		logAddFailure("device", shortNpub(j.DeviceNpub), err)
		return false, errors.New("Couldn't add the device. " + plainPanelError(err))
	}
	fmt.Fprintf(t.out, "  Added %s. Connect Nostr VPN on it and it uses this box.\n", name)
	return true, nil
}

func (c config) termAddBox(ctx context.Context, t setupIO, p page, raw string) (bool, error) {
	npub, asBackup, already, err := checkAddBox(p, raw)
	if err != nil {
		return false, err
	}
	if already {
		fmt.Fprintf(t.out, "  The box %s is already on this network.\n", npub)
		return false, nil
	}
	role := "a member"
	if asBackup {
		role = "a backup DNS box, so DNS keeps working when this box is off"
	}
	fmt.Fprintf(t.out, "  This is a box with ID %s. It joins as %s.\n", npub, role)
	if !t.yes(fmt.Sprintf("  Add it to %s? [y/N] ", networkLabel(p))) {
		fmt.Fprintln(t.out, "  Not added.")
		return false, nil
	}
	if err := c.addBox(ctx, p, npub, "DNS box 2", asBackup); err != nil {
		return false, err
	}
	fmt.Fprintln(t.out, "  Added the box.")
	return true, nil
}

func (c config) secondBox(ctx context.Context, t setupIO, o setupOptions) error {
	fmt.Fprintln(t.out)
	fmt.Fprintln(t.out, "Join your first box")
	fmt.Fprintln(t.out, "  On your first box, open its setup (or dashboard): Add your next device, Another DNS box.")
	fmt.Fprintln(t.out, "  It shows a join code like  npub1…:a1b2c3d4")
	for {
		raw, ok := t.line("Paste the first box's join code, or press Enter to skip: ")
		if !ok || raw == "" {
			fmt.Fprintln(t.out, "Skipped. "+dashboardLater)
			return nil
		}
		err := c.joinFirstBox(ctx, raw)
		if err == nil {
			break
		}
		fmt.Fprintln(t.out, "  "+err.Error())
		var ae *actionError
		if errors.As(err, &ae) && ae.status == http.StatusConflict {
			return nil // nothing more to try here; the message says what to do
		}
	}
	return c.secondBoxWait(ctx, t, o, c.buildPage(ctx))
}

// secondBoxWait shows this box's ID for the first box to add, and waits until
// it has. Enter stops waiting; the first box can still add it later.
func (c config) secondBoxWait(ctx context.Context, t setupIO, o setupOptions, p page) error {
	if p.Admitted {
		fmt.Fprintln(t.out, "Device setup: this box has joined your first box's network.")
		return nil
	}
	fmt.Fprintln(t.out)
	fmt.Fprintln(t.out, "Now let your first box add this one. On the first box, paste this ID")
	fmt.Fprintln(t.out, "(in its terminal setup, or its dashboard under Add another DNS box):")
	fmt.Fprintln(t.out, "    "+p.OwnNpub)
	c.printQR(ctx, t.out, deviceQRPrefix+p.OwnNpub)
	fmt.Fprintln(t.out, "Waiting for your first box to add this one (press Enter to stop waiting)...")

	stop := make(chan struct{})
	go func() {
		t.in.ReadString('\n')
		close(stop)
	}()
	deadline := time.Now().Add(o.admitted)
	for time.Now().Before(deadline) {
		if c.buildPage(ctx).Admitted {
			fmt.Fprintln(t.out, "Done: your first box added this one. You're set.")
			return nil
		}
		select {
		case <-stop:
			fmt.Fprintln(t.out, "Stopped waiting. The first box can still add this one: paste the ID above there.")
			return nil
		case <-time.After(o.poll):
		}
	}
	fmt.Fprintln(t.out, "Still waiting. The first box can still add this one: paste the ID above there.")
	return nil
}

func networkLabel(p page) string {
	if p.NetworkName != "" {
		return "the network \"" + p.NetworkName + "\""
	}
	return "this network"
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// printQR draws text as a QR code with half blocks, two modules per character
// row, light on dark so a phone reads it off an ordinary dark terminal. The
// matrix comes from the control panel's own /api/qr_matrix; if the panel
// can't make one, the text printed next to it is enough.
func (c config) printQR(ctx context.Context, out io.Writer, text string) {
	m, err := c.qrMatrix(ctx, text)
	if err != nil || m.Width == 0 || len(m.Cells) != m.Width*m.Width {
		return
	}
	fmt.Fprint(out, renderQR(m.Width, m.Cells))
}

type qrMatrix struct {
	Width int    `json:"width"`
	Cells []bool `json:"cells"`
}

func (c config) qrMatrix(ctx context.Context, text string) (qrMatrix, error) {
	var m qrMatrix
	body, _ := json.Marshal(map[string]string{"text": text})
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.panelAPI+"/api/qr_matrix", bytes.NewReader(body))
	if err != nil {
		return m, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("control panel returned %s", resp.Status)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m)
	return m, err
}

// renderQR: dark modules are spaces, light ones (and a 2-module quiet zone)
// are blocks. Each character covers two rows: top in the upper half block,
// bottom in the lower.
func renderQR(width int, cells []bool) string {
	const quiet = 2
	dark := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		if x < 0 || y < 0 || x >= width || y >= width {
			return false
		}
		return cells[y*width+x]
	}
	size := width + 2*quiet
	var b strings.Builder
	for y := 0; y < size; y += 2 {
		b.WriteString("    ")
		for x := 0; x < size; x++ {
			top, bottom := !dark(x, y), y+1 < size && !dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
