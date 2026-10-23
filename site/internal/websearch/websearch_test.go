package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dashboard/internal/egress"
)

func TestText(t *testing.T) {
	page := `<html><head><title>x</title><style>p{}</style></head><body>
<script>alert(1)</script><h1>Oats &amp; protein</h1><p>One cup has  <b>10 g</b>.</p>
<ul><li>Fiber</li><li>Iron</li></ul><!-- hidden --></body></html>`
	got := Text(page)
	want := "Oats & protein\nOne cup has 10 g .\n\n- Fiber\n\n- Iron"
	if got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

func TestSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" || r.URL.Query().Get("q") != "oat calories" {
			t.Errorf("asked %s", r.URL)
		}
		w.Write([]byte(`{"results":[{"title":"Oats","url":"https://example.com/oats","content":"389 kcal"}]}`))
	}))
	defer server.Close()
	results, err := Search(context.Background(), server.Client(), server.URL, "oat calories")
	if err != nil || len(results) != 1 || results[0].URL != "https://example.com/oats" {
		t.Fatalf("Search() = %v, %v", results, err)
	}
	if text := Format(results); !strings.Contains(text, "1. Oats\nhttps://example.com/oats\n389 kcal") {
		t.Errorf("Format() = %q", text)
	}
	if _, err := Search(context.Background(), server.Client(), "", "x"); err == nil {
		t.Error("searched without a SearXNG")
	}
}

func TestSearchJSONOff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	if _, err := Search(context.Background(), server.Client(), server.URL, "x"); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Errorf("Search() error = %v, want one about the JSON format", err)
	}
}

// Pages on the local network (like the dashboard itself) can't be read.
func TestFetchOnlyPublic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the local server was reached")
	}))
	defer server.Close()
	client := egress.New().Client(5 * time.Second)
	for _, address := range []string{server.URL, "http://localhost/", "file:///etc/passwd", "http://169.254.169.254/latest"} {
		if _, err := Fetch(context.Background(), client, address); err == nil {
			t.Errorf("Fetch(%q) worked", address)
		}
	}
}

func TestFetchText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<p>Hello <i>there</i></p>" + strings.Repeat("a", maxPageText)))
	}))
	defer server.Close()
	text, err := Fetch(context.Background(), server.Client(), server.URL)
	if err != nil || !strings.HasPrefix(text, "Page: "+server.URL) || !strings.Contains(text, "Hello there") ||
		!strings.HasSuffix(text, "[The page goes on; this is its first part.]") {
		t.Errorf("Fetch() = %.120q…, %v", text, err)
	}
}
