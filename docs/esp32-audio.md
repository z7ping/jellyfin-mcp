# Playing Jellyfin music on an ESP32 / external MCP player

This opt-in extension exposes **playback resources**, not Jellyfin remote-control sessions.

## Run Jellyfin MCP with an audio data endpoint

```sh
JELLYFIN_URL=http://jellyfin:8096 \
JELLYFIN_API_KEY=YOUR_KEY \
jellyfin-mcp --http --addr 0.0.0.0:8080 \
  --http-token YOUR_MCP_BEARER_TOKEN \
  --audio-public-url http://mcp-host:8080 \
  --toolsets discovery,media,user,playback
```

Use the reachable host or reverse-proxy URL for `--audio-public-url`. This URL must route to this MCP server's `/audio/` endpoint; the ESP32 Go server needs to reach it. **Do not put the Jellyfin API key in that URL.** The URL is not the Jellyfin server URL.

For Compose use `JELLYFIN_MCP_AUDIO_PUBLIC_URL=http://mcp-host:8080`. The feature is **disabled by default**. It requires HTTP mode; stdio-only deployments still expose the original MCP tools but cannot supply this HTTP audio endpoint. An HTTPS reverse proxy is recommended outside a trusted LAN.

## Tools and playback

Existing tools remain available:
- `jellyfin_search` for songs and IDs.
- `jellyfin_playlists` `list` and `get` for playlists.
- `jellyfin_music` for artists and instant mixes.

New `jellyfin_audio` (read-only, part of `media` toolset):
- `{"item_id":"<audio-item-id>"}` returns one `audio/mpeg` ResourceLink.
- `{"playlist_id":"<playlist-id>"}` returns ResourceLinks **in playlist order**, up to 1000 tracks. Non-audio entries are skipped; a playlist larger than this limit is rejected, not silently truncated.

An MCP-compatible media consumer receives the `ResourceLink` and streams the `http(s)://.../audio/<opaque-token>` URI directly. It must not call `jellyfin_play`, which controls a *different* Jellyfin client session.

## Security and limitations

- Links have 256-bit random bearer tokens, are scoped to one track and expire after eight hours. Possession of the URL grants playback until it expires; never log or repeat it to users.
- The proxy injects the Jellyfin API key server-side and streams MP3 without buffering whole tracks.
- The proxy endpoint accepts GET only. No direct Jellyfin address or API key is returned. Revoking access early currently requires restarting the MCP server.
- Range/seek and transparent replay after the token expires are not implemented; songs should be played within the link lifetime.
- Source files are served as MP3 using Jellyfin's transcoding capability. Jellyfin's server must support that transcode.
- A large playlist issues links up front; future work should refresh links just in time for very long playback sessions.
- Keep `--http-token` configured for the MCP control endpoint and restrict access to the audio data endpoint to the local network or reverse proxy.
- No hardware/ESP32 firmware changes are required.
