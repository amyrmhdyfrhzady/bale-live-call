package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"bale-live-call/media"
	"bale-live-call/relay/bale"
	"bale-live-call/relay/common"
	"net/http"
)

const creatorOrigin = "https://web.bale.ai"

type creator struct {
	cookieStr                  string
	config                     BaleConfig
	streamURL                  string
	width, height, fps         int
	videoBitrate, audioBitrate string
	videoPort, audioPort       int
	mu                         sync.Mutex
	link                       string
}

type BaleConfig struct {
	APIVersion int64
	WSURL      string
}

func fetchConfig() (BaleConfig, error) {
	var cfg BaleConfig
	page, err := common.HttpGet("https://web.bale.ai/")
	if err != nil {
		return cfg, fmt.Errorf("fetch web.bale.ai: %w", err)
	}
	bundlePath := findBundlePath(string(page))
	if bundlePath == "" {
		return cfg, fmt.Errorf("index bundle not found")
	}
	bundle, err := common.HttpGet("https://web.bale.ai" + bundlePath)
	if err != nil {
		return cfg, fmt.Errorf("fetch bundle: %w", err)
	}
	cfg.WSURL = findWS(string(bundle))
	if cfg.WSURL == "" {
		return cfg, fmt.Errorf("ws url not found")
	}
	cfg.APIVersion = findAPIVersion(string(bundle))
	if cfg.APIVersion == 0 {
		return cfg, fmt.Errorf("apiVersion not found")
	}
	log.Printf("[config] ws=%s apiVersion=%d", cfg.WSURL, cfg.APIVersion)
	return cfg, nil
}

func findBundlePath(s string) string {
	for i := 0; i+12 < len(s); i++ {
		const prefix = "/static/js/index."
		if strings.HasPrefix(s[i:], prefix) {
			j := i
			for j < len(s) && s[j] != '"' && s[j] != '\'' && s[j] != '?' && s[j] != '\\' {
				j++
			}
			p := s[i:j]
			if strings.HasSuffix(p, ".js") {
				return p
			}
		}
	}
	return ""
}

func findWS(s string) string {
	key := `"wss://`
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	start := i + 1
	end := strings.IndexByte(s[start:], '"')
	if end < 0 {
		return ""
	}
	return s[start : start+end]
}

func findAPIVersion(s string) int64 {
	key := `Number("`
	i := strings.Index(s, key)
	if i < 0 {
		return 0
	}
	start := i + len(key)
	end := strings.Index(s[start:], `")`)
	if end < 0 {
		return 0
	}
	var v int64
	fmt.Sscan(s[start:start+end], &v)
	return v
}

func (c *creator) dialBridge() (*bale.Bridge, error) {
	bridge := bale.NewBridge(bale.BridgeConfig{LogFn: log.Printf})
	h := http.Header{}
	h.Set("User-Agent", common.UserAgent)
	h.Set("Origin", creatorOrigin)
	h.Set("Cookie", c.cookieStr)
	if err := bridge.Dial(c.config.WSURL, h); err != nil {
		return nil, err
	}
	go bridge.Run()
	bridge.SendHandshake(c.config.APIVersion)
	select {
	case <-bridge.Hello():
		return bridge, nil
	case <-time.After(10 * time.Second):
		bridge.Close()
		return nil, fmt.Errorf("Bale handshake timeout")
	}
}

func (c *creator) run(ctx context.Context) {
	bridge, err := c.dialBridge()
	if err != nil {
		log.Fatalf("[bale-ws] %s", common.MaskError(err))
	}
	resp, err := bridge.Unary("bale.meet.v1.Meet", "GenerateCallLink", bale.EncodeGenerateCallLinkRequest(true))
	if err != nil {
		log.Fatalf("[auth] %v", err)
	}
	call, err := bale.DecodeCallEnvelope(resp.Response)
	if err != nil {
		log.Fatalf("[auth] decode: %v", err)
	}
	c.mu.Lock()
	c.link = call.ShareLink
	c.mu.Unlock()
	fmt.Println("\nCALL CREATED")
	fmt.Println("join_link:", call.ShareLink)
	fmt.Println("stream:", c.streamURL)

	first := true
	for {
		if !first {
			bridge, err = c.dialBridge()
			if err != nil {
				log.Printf("[bale-ws] %s; retrying in 5s", common.MaskError(err))
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					continue
				}
			}
		}
		first = false
		joinedResp, err := bridge.Unary("bale.meet.v1.Meet", "JoinGroupCall", bale.EncodeJoinGroupCallRequest(call.ID, "Iran International Live"))
		if err != nil {
			log.Printf("[auth] JoinGroupCall: %v", err)
			bridge.Close()
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}
		joined, err := bale.DecodeCallEnvelope(joinedResp.Response)
		if err != nil || joined.URL == "" || joined.LivekitJWT == "" {
			if err == nil {
				err = fmt.Errorf("empty LiveKit credentials")
			}
			log.Printf("[auth] join decode: %v", err)
			bridge.Close()
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}
		log.Printf("[auth] livekit url=%s room=%s", joined.URL, joined.Token)

		sess := bale.NewMediaSession(bale.MediaSessionConfig{
			WSURL: joined.URL, RoomToken: joined.LivekitJWT, Origin: "https://meet.bale.ai", LogFn: log.Printf,
			StreamURL: c.streamURL, Width: c.width, Height: c.height, FPS: c.fps,
			VideoBitrate: c.videoBitrate, AudioBitrate: c.audioBitrate,
			VideoPort: c.videoPort, AudioPort: c.audioPort,
		})
		if err := sess.Start(ctx); err != nil {
			log.Printf("[session] start: %v", err)
			sess.Close()
			bridge.Close()
			continue
		}
		select {
		case <-ctx.Done():
			sess.Close()
			bridge.Close()
			return
		case <-sess.Done():
		}
		sess.Close()
		bridge.Close()
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (c *creator) currentLink() string { c.mu.Lock(); defer c.mu.Unlock(); return c.link }

func main() {
	cookiesPath := flag.String("cookies", "", "path to bale-cookies.json")
	cookieString := flag.String("cookie-string", "", "raw cookie string")
	streamURL := flag.String("stream-url", media.BuildDefaultStreamURL(), "HLS/DASH stream URL")
	resources := flag.String("resources", "default", "memory mode: default, moderate, unlimited")
	writeFile := flag.String("write-file", "", "write active call link to this file")
	flag.Parse()

	switch *resources {
	case "moderate":
		debug.SetMemoryLimit(64 << 20)
	case "default":
		debug.SetMemoryLimit(128 << 20)
	case "unlimited":
		debug.SetMemoryLimit(256 << 20)
	default:
		log.Fatalf("unknown resources mode: %s", *resources)
	}
	common.MaskingEnabled = true

	var cookieStr string
	if *cookieString != "" {
		cookieStr = *cookieString
	} else if *cookiesPath != "" {
		cookieStr = common.LoadCookies(*cookiesPath)
	} else {
		fmt.Println("WAITING_FOR_COOKIES")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil || strings.TrimSpace(line) == "" {
			log.Fatal("No cookies received on stdin")
		}
		cookieStr = strings.TrimSpace(line)
	}

	cfg, err := fetchConfig()
	if err != nil {
		log.Fatal(err)
	}
	c := &creator{cookieStr: cookieStr, config: cfg, streamURL: *streamURL, width: media.EnvInt("VIDEO_WIDTH", 1280), height: media.EnvInt("VIDEO_HEIGHT", 720), fps: media.EnvInt("VIDEO_FPS", 24), videoBitrate: getenv("VIDEO_BITRATE", "1400k"), audioBitrate: getenv("AUDIO_BITRATE", "64k"), videoPort: media.EnvInt("VIDEO_RTP_PORT", 5004), audioPort: media.EnvInt("AUDIO_RTP_PORT", 5006)}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	media.InstallSignalHandler(cancel)

	if *writeFile != "" {
		go func() {
			for c.currentLink() == "" {
				time.Sleep(50 * time.Millisecond)
			}
			if err := os.WriteFile(*writeFile, []byte(c.currentLink()+"\n"), 0644); err != nil {
				log.Printf("write link: %v", err)
			}
		}()
	}
	c.run(ctx)
}

func getenv(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}
