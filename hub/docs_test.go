package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The docs container is opt-in. Without it the dashboard still links to the
// guide, on GitHub, and shows no Docs tile: a red "down" for a container the
// install never started would read as something broken.
func TestDocsOffLinksOnlineGuide(t *testing.T) {
	body := get(t, newMux(deadConfig(t)), "/dashboard").Body.String()
	if !strings.Contains(body, `href="`+onlineGuideURL+`"`) {
		t.Fatalf("no link to the online guide with the docs container off:\n%s", body)
	}
	if strings.Contains(body, `<span class="name">Docs</span><br><span class="detail">down`) {
		t.Fatalf("a Docs down tile with the docs container off:\n%s", body)
	}
}

// With the docs profile on, the dashboard links to the box's own copy, which
// is what reading offline needs.
func TestDocsOnLinksLocalCopy(t *testing.T) {
	docs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer docs.Close()
	cfg := deadConfig(t)
	cfg.docsAddr = strings.TrimPrefix(docs.URL, "http://")
	body := get(t, newMux(cfg), "/dashboard").Body.String()
	if strings.Contains(body, onlineGuideURL) {
		t.Fatalf("links the online guide while the box serves its own docs:\n%s", body)
	}
	// A test request is not from the box itself, so the box's docs come
	// through the hub (docsProxy).
	if !strings.Contains(body, `href="/docs/"`) {
		t.Fatalf("no link to the box's own docs through the hub:\n%s", body)
	}
	if !strings.Contains(body, `<span class="name">Docs</span><br><span class="detail">up`) {
		t.Fatalf("no Docs up tile:\n%s", body)
	}
}
