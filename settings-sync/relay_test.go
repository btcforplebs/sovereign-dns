package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// A hostile relay that ignores the limit and streams events without end only
// gets maxFetchEvents into the box.
func TestFetchListsIsCapped(t *testing.T) {
	var sawLimit float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var req []json.RawMessage
		_ = json.Unmarshal(data, &req)
		var sub string
		var filter map[string]any
		_ = json.Unmarshal(req[1], &sub)
		_ = json.Unmarshal(req[2], &filter)
		sawLimit, _ = filter["limit"].(float64)
		ev, _ := json.Marshal(event{Kind: settingsKind, Content: strings.Repeat("x", 1000)})
		msg := []byte(`["EVENT","` + sub + `",` + string(ev) + `]`)
		for i := 0; i < 10000; i++ {
			if c.Write(ctx, websocket.MessageText, msg) != nil {
				return
			}
		}
	}))
	defer srv.Close()
	evs, err := fetchLists(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != maxFetchEvents || sawLimit != fetchLimit {
		t.Fatalf("got %d events, limit %v", len(evs), sawLimit)
	}
}
