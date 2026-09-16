# Bale Live Call

A headless Bale participant that creates a Bale video call and publishes a live TV stream as its camera feed.

## What it does

`Bale -> headless creator -> LiveKit/WebRTC publisher -> FFmpeg -> HLS -> VP8/Opus -> Bale call`

The server is one participant in the call. When another participant joins the generated link, they receive the live stream as the server's video and audio.

## Requirements

- Linux VPS/server
- Go 1.23+
- FFmpeg 7.x or newer
- Bale cookies exported from a logged-in Bale web session

## Run

```bash
go build -o bale-live-call ./cmd/bale-live-call
./bale-live-call --cookies bale-cookies.json
```

Or:

```bash
STREAM_URL='https://hlspackager.akamaized.net/live/DB/IRAN_INTERNATIONAL/HLS/IRAN_INTERNATIONAL.m3u8' ./bale-live-call --cookies bale-cookies.json
```

The program prints a `join_link`. Open that link in Bale to join the call.

## Environment variables

- `STREAM_URL`: HLS/DASH input. Defaults to the current Iran International HLS endpoint used by the project.
- `VIDEO_WIDTH`: default `1280`
- `VIDEO_HEIGHT`: default `720`
- `VIDEO_FPS`: default `24`
- `VIDEO_BITRATE`: default `1400k`
- `AUDIO_BITRATE`: default `64k`
- `VIDEO_RTP_PORT`: default `5004`
- `AUDIO_RTP_PORT`: default `5006`

## Cookies

The creator uses the same Bale web-session cookie format as the reference project. The cookie file is a JSON array containing objects with `name` and `value`.

## License

The Bale signaling/WebRTC portions are adapted from `kulikov0/whitelist-bypass-iran`, MIT licensed.
