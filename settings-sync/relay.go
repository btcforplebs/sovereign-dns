package main

// Talking to relays: fetch the shared list, publish a new one. One short
// connection per call; the box polls, so nothing here has to stay connected.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/coder/websocket"
)

const relayTimeout = 15 * time.Second

func dialRelay(ctx context.Context, url string) (*websocket.Conn, error) {
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(4 << 20)
	return c, nil
}

func writeJSON(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, b)
}

// fetchLists returns every settings event the relay has from owner, up to EOSE.
func fetchLists(ctx context.Context, url, owner string) ([]*event, error) {
	ctx, cancel := context.WithTimeout(ctx, relayTimeout)
	defer cancel()
	c, err := dialRelay(ctx, url)
	if err != nil {
		return nil, err
	}
	defer c.CloseNow()
	idb := make([]byte, 8)
	_, _ = rand.Read(idb)
	sub := "sdns-" + hex.EncodeToString(idb)
	filter := map[string]any{"kinds": []int{settingsKind}, "authors": []string{owner}, "#d": []string{settingsD}}
	if err := writeJSON(ctx, c, []any{"REQ", sub, filter}); err != nil {
		return nil, err
	}
	var out []*event
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return out, err
		}
		var msg []json.RawMessage
		if json.Unmarshal(data, &msg) != nil || len(msg) < 2 {
			continue
		}
		var typ, id string
		_ = json.Unmarshal(msg[0], &typ)
		_ = json.Unmarshal(msg[1], &id)
		if id != sub {
			continue
		}
		switch typ {
		case "EVENT":
			if len(msg) >= 3 {
				var ev event
				if json.Unmarshal(msg[2], &ev) == nil {
					out = append(out, &ev)
				}
			}
		case "EOSE":
			_ = writeJSON(ctx, c, []any{"CLOSE", sub})
			c.Close(websocket.StatusNormalClosure, "")
			return out, nil
		case "CLOSED":
			return out, fmt.Errorf("relay closed the subscription: %s", string(data))
		}
	}
}

func publishEvent(ctx context.Context, url string, ev *event) error {
	ctx, cancel := context.WithTimeout(ctx, relayTimeout)
	defer cancel()
	c, err := dialRelay(ctx, url)
	if err != nil {
		return err
	}
	defer c.CloseNow()
	if err := writeJSON(ctx, c, []any{"EVENT", ev}); err != nil {
		return err
	}
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return err
		}
		var msg []json.RawMessage
		if json.Unmarshal(data, &msg) != nil || len(msg) < 3 {
			continue
		}
		var typ, id string
		var ok bool
		_ = json.Unmarshal(msg[0], &typ)
		_ = json.Unmarshal(msg[1], &id)
		if typ != "OK" || id != ev.ID {
			continue
		}
		_ = json.Unmarshal(msg[2], &ok)
		c.Close(websocket.StatusNormalClosure, "")
		if !ok {
			reason := ""
			if len(msg) > 3 {
				_ = json.Unmarshal(msg[3], &reason)
			}
			return errors.New("relay refused: " + reason)
		}
		return nil
	}
}
