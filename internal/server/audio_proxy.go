package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	jf "github.com/jaredtrent/jellyfin-mcp/internal/jellyfin"
	"github.com/jaredtrent/jellyfin-mcp/internal/server/tools"
)

const (
	audioLinkTTL       = 8 * time.Hour
	maxAudioLinkCount  = 4096
	maxPlaylistTracks  = 1000
)

type audioStreamOpener interface {
	OpenAudioStream(context.Context, string) (io.ReadCloser, error)
}

type audioGrant struct {
	itemID  string
	expires time.Time
}

// audioProxy converts an MCP resource discovery result to an HTTP data stream.
// Only unguessable, expiring per-track tokens leave the MCP process; neither
// the Jellyfin API key nor the original Jellyfin URL is disclosed.
type audioProxy struct {
	client jf.Client
	opener audioStreamOpener
	baseURL string
	mu sync.Mutex
	grants map[string]audioGrant
}

func newAudioProxy(client jf.Client, baseURL string) (*audioProxy, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("audio-public-url must be an absolute HTTP(S) origin or path without credentials or query")
	}
	opener, ok := client.(audioStreamOpener)
	if !ok {
		return nil, fmt.Errorf("Jellyfin client cannot stream audio")
	}
	return &audioProxy{client: client, opener: opener, baseURL: strings.TrimRight(u.String(), "/"), grants: make(map[string]audioGrant)}, nil
}

func (p *audioProxy) link(itemID, title string) (*mcp.ResourceLink, error) {
	itemID = jf.NormalizeID(itemID)
	if itemID == "" {
		return nil, fmt.Errorf("empty Jellyfin item ID")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	p.mu.Lock()
	now := time.Now()
	for k, v := range p.grants {
		if !now.Before(v.expires) {
			delete(p.grants, k)
		}
	}
	if len(p.grants) >= maxAudioLinkCount {
		p.mu.Unlock()
		return nil, fmt.Errorf("too many pending audio links; retry later")
	}
	p.grants[token] = audioGrant{itemID: itemID, expires: now.Add(audioLinkTTL)}
	p.mu.Unlock()
	return &mcp.ResourceLink{
		URI: p.baseURL + "/audio/" + token,
		Name: title,
		Title: title,
		MIMEType: "audio/mpeg",
	}, nil
}

type audioToolArgs struct {
	ItemID string `json:"item_id,omitempty" jsonschema:"Single audio track ID returned by jellyfin_search"`
	PlaylistID string `json:"playlist_id,omitempty" jsonschema:"Playlist ID returned by jellyfin_search with type Playlist"`
}

func (p *audioProxy) registerTool(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "jellyfin_audio",
		Title: "Audio for External Player",
		Description: "Return playable audio ResourceLinks for a track (item_id) or ordered playlist (playlist_id). Find playlists using jellyfin_search with type Playlist, including in read-only mode. "+
			"Use this for playing music on the assistant's own speaker. Unlike jellyfin_play it needs no Jellyfin client session. "+
			"Each resource link is temporary and accessible only to a holder of its opaque token; do not repeat URLs to the user.",
		InputSchema: jf.WithEnums[audioToolArgs](nil),
		Annotations: tools.AnnotReadOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args audioToolArgs) (*mcp.CallToolResult, any, error) {
		if (args.ItemID == "") == (args.PlaylistID == "") {
			return jf.ErrResult("Specify exactly one of item_id or playlist_id."), nil, nil
		}
		userID, err := p.client.GetUserID(ctx)
		if err != nil {
			return jf.ErrResult("Could not resolve Jellyfin user: %v", err), nil, nil
		}
		var tracks []jf.MediaItem
		if args.ItemID != "" {
			var item map[string]any
			err = p.client.Get(ctx, "/Items/"+jf.SanitizeID(args.ItemID), url.Values{"UserId": {userID}}, &item)
			if err != nil {
				return jf.ErrResult("Could not find audio item: %v", err), nil, nil
			}
			tracks = []jf.MediaItem{jf.MediaItemFrom(item)}
		} else {
			// Read from the same Jellyfin endpoint used by jellyfin_playlists
			// without asking an LLM to round-trip hundreds of item IDs.
			endpoint := "/Playlists/"+jf.SanitizeID(args.PlaylistID)+"/Items"
			raw, total, err := jf.FetchAllPages(ctx, p.client, endpoint, url.Values{"UserId": {userID}}, maxPlaylistTracks)
			if err != nil {
				return jf.ErrResult("Could not load playlist: %v", err), nil, nil
			}
			if total > len(raw) {
				return jf.ErrResult("Playlist has %d tracks; maximum supported is %d. Refusing to play a truncated playlist.", total, maxPlaylistTracks), nil, nil
			}
			tracks = jf.MediaItemsFrom(raw)
		}

		result := &mcp.CallToolResult{Content: []mcp.Content{}}
		for _, track := range tracks {
			if track.Type != "Audio" {
				continue
			}
			link, err := p.link(track.ID, track.Name)
			if err != nil {
				return jf.ErrResult("Could not issue audio link: %v", err), nil, nil
			}
			result.Content = append(result.Content, link)
		}
		if len(result.Content) == 0 {
			return jf.ErrResult("No playable audio tracks were found."), nil, nil
		}
		return result, nil, nil
	})
}

// ServeHTTP is the data plane. It proxies a single authenticated Jellyfin MP3
// stream with constant memory; a URL never redirects to Jellyfin or exposes a key.
func (p *audioProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/audio/")
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	p.mu.Lock()
	grant, ok := p.grants[token]
	p.mu.Unlock()
	if !ok || !time.Now().Before(grant.expires) {
		http.NotFound(w, r)
		return
	}
	audio, err := p.opener.OpenAudioStream(r.Context(), grant.itemID)
	if err != nil {
		http.Error(w, "audio unavailable", http.StatusBadGateway)
		return
	}
	defer audio.Close()
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, audio)
}
