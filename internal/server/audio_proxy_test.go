package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	jf "github.com/jaredtrent/jellyfin-mcp/internal/jellyfin"
)

type audioTestClient struct {
	jf.Client
	lastID string
	data string
}

func (c *audioTestClient) OpenAudioStream(_ context.Context, itemID string) (io.ReadCloser, error) {
	c.lastID = itemID
	if c.data == "" {
		return nil, errors.New("upstream unavailable")
	}
	return io.NopCloser(strings.NewReader(c.data)), nil
}

func TestAudioProxyStreamsMP3WithoutExposingKey(t *testing.T) {
	client := &audioTestClient{data: "pretend MP3 bytes"}
	proxy, err := newAudioProxy(client, "http://mcp.example.local:8080")
	if err != nil {
		t.Fatal(err)
	}
	link, err := proxy.link("0123456789abcdef0123456789abcdef", "Song A")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link.URI, "http://mcp.example.local:8080/audio/") {
		t.Fatalf("unexpected link location: %s", link.URI)
	}
	if strings.Contains(link.URI, "0123456789abcdef0123456789abcdef") {
		t.Fatal("link exposes Jellyfin item ID")
	}
	u, _ := url.Parse(link.URI)
	r := httptest.NewRequest(http.MethodGet, u.Path, nil)
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != client.data {
		t.Fatalf("audio proxy returned %d %q", w.Code, w.Body.String())
	}
	if client.lastID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("forwarded itemID = %q", client.lastID)
	}
	if w.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("unexpected MIME type: %s", w.Header().Get("Content-Type"))
	}
}

func TestAudioProxyRejectsMissingExpiredTokensAndPost(t *testing.T) {
	client := &audioTestClient{data: "audio"}
	proxy, err := newAudioProxy(client, "http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	link, err := proxy.link("0123456789abcdef0123456789abcdef", "Song A")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link.URI)
	for _, path := range []string{"/audio/missing", "/audio/", "/audio/../secret"} {
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s returned %d, want 404", path, w.Code)
		}
	}
	token := strings.TrimPrefix(u.Path, "/audio/")
	proxy.mu.Lock()
	proxy.grants[token] = audioGrant{itemID: "0123456789abcdef0123456789abcdef", expires: time.Now().Add(-time.Second)}
	proxy.mu.Unlock()
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, httptest.NewRequest(http.MethodGet, u.Path, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expired token returned %d", w.Code)
	}
	w = httptest.NewRecorder()
	proxy.ServeHTTP(w, httptest.NewRequest(http.MethodPost, u.Path, nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST returned %d", w.Code)
	}
}

func TestAudioProxyRequiresExplicitPublicURL(t *testing.T) {
	client := &audioTestClient{}
	for _, input := range []string{"", "/audio", "file:///tmp/x", "https://user:pass@example.com", "http://example.com?key=secret"} {
		if _, err := newAudioProxy(client, input); err == nil {
			t.Errorf("accepted invalid public URL %q", input)
		}
	}
}
