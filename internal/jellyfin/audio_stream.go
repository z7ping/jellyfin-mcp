package jellyfin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
)

// Audio streams are requested by the MCP server, never by the assistant or the
// downstream player. The Jellyfin API key stays in this process.
var audioItemIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{32,36}$`)

// OpenAudioStream returns a streaming MP3 response. Unlike the normal JSON API
// client it does not buffer the response or impose a 30-second whole-song timeout.
func (c *JellyfinClient) OpenAudioStream(ctx context.Context, itemID string) (io.ReadCloser, error) {
	if !audioItemIDPattern.MatchString(itemID) {
		return nil, fmt.Errorf("invalid audio item ID")
	}
	u, err := url.JoinPath(c.baseURL, "Audio", itemID, "stream.mp3")
	if err != nil {
		return nil, fmt.Errorf("build audio URL: %w", err)
	}
	q := url.Values{
		"static":       {"false"},
		"audioCodec":   {"mp3"},
		"audioBitRate": {"128000"},
		"audioChannels": {"2"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.authorizationHeader())
	req.Header.Set("Accept", "audio/mpeg")
	// A streaming response must not inherit the JSON client's 30s timeout.
	streamClient := &http.Client{Transport: c.httpClient.Transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		// Never forward credentials to another host via redirects.
		if len(via) != 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("cross-host audio redirect refused")
		}
		return nil
	}}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Jellyfin audio request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("Jellyfin audio HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}
