package jellyfin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAudioStreamAuthenticatesAndStreams(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	var receivedPath, receivedKey, receivedCodec string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedKey = r.Header.Get("Authorization")
		receivedCodec = r.URL.Query().Get("audioCodec")
		w.Header().Set("Content-Type", "audio/mpeg")
		io.WriteString(w, "mp3 stream")
	}))
	defer server.Close()
	client := &JellyfinClient{baseURL: server.URL, apiKey: "do-not-share", httpClient: server.Client()}
	body, err := client.OpenAudioStream(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	result, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != "mp3 stream" {
		t.Fatalf("result = %q", result)
	}
	if receivedPath != "/Audio/"+id+"/stream.mp3" || receivedCodec != "mp3" {
		t.Fatalf("unexpected audio request: %q codec=%q", receivedPath, receivedCodec)
	}
	if !strings.Contains(receivedKey, "do-not-share") {
		t.Fatal("Jellyfin request did not include API authorization")
	}
}

func TestOpenAudioStreamRejectsInvalidID(t *testing.T) {
	client := &JellyfinClient{baseURL: "http://localhost", apiKey: "token", httpClient: &http.Client{}}
	if _, err := client.OpenAudioStream(context.Background(), "../../danger"); err == nil {
		t.Fatal("audio item ID traversal should fail")
	}
}

func TestOpenAudioStreamRejectsUpstreamErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no audio", http.StatusNotFound)
	}))
	defer server.Close()
	client := &JellyfinClient{baseURL: server.URL, apiKey: "token", httpClient: server.Client()}
	if _, err := client.OpenAudioStream(context.Background(), "0123456789abcdef0123456789abcdef"); err == nil {
		t.Fatal("upstream 404 should fail")
	}
}
