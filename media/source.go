package media

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type Config struct {
	StreamURL    string
	Width        int
	Height       int
	FPS          int
	VideoBitrate string
	AudioBitrate string
	VideoPort    int
	AudioPort    int
	LogFn        func(string, ...any)
}

type Source struct {
	cfg   Config
	video *webrtc.TrackLocalStaticRTP
	audio *webrtc.TrackLocalStaticRTP
}

func New(cfg Config, video *webrtc.TrackLocalStaticRTP, audio *webrtc.TrackLocalStaticRTP) *Source {
	if cfg.LogFn == nil {
		cfg.LogFn = log.Printf
	}
	if cfg.Width == 0 {
		cfg.Width = 1280
	}
	if cfg.Height == 0 {
		cfg.Height = 720
	}
	if cfg.FPS == 0 {
		cfg.FPS = 24
	}
	if cfg.VideoBitrate == "" {
		cfg.VideoBitrate = "1400k"
	}
	if cfg.AudioBitrate == "" {
		cfg.AudioBitrate = "64k"
	}
	if cfg.VideoPort == 0 {
		cfg.VideoPort = 5004
	}
	if cfg.AudioPort == 0 {
		cfg.AudioPort = 5006
	}
	return &Source{cfg: cfg, video: video, audio: audio}
}

func (s *Source) Run(ctx context.Context) error {
	if strings.TrimSpace(s.cfg.StreamURL) == "" {
		return fmt.Errorf("STREAM_URL is empty")
	}

	videoConn, err := listenRTP(s.cfg.VideoPort)
	if err != nil {
		return err
	}
	defer videoConn.Close()
	audioConn, err := listenRTP(s.cfg.AudioPort)
	if err != nil {
		return err
	}
	defer audioConn.Close()

	for {
		if err := s.runOnce(ctx, videoConn, audioConn); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.cfg.LogFn("[media] ffmpeg session ended: %v; restarting in 3s", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
		}
	}
}

func (s *Source) runOnce(ctx context.Context, videoConn, audioConn *net.UDPConn) error {
	videoAddr := videoConn.LocalAddr().(*net.UDPAddr)
	audioAddr := audioConn.LocalAddr().(*net.UDPAddr)
	cmdArgs := []string{
		"-hide_banner", "-loglevel", "warning",
		"-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "5",
		"-i", s.cfg.StreamURL,
		"-map", "0:v:0", "-an",
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,fps=%d", s.cfg.Width, s.cfg.Height, s.cfg.Width, s.cfg.Height, s.cfg.FPS),
		"-c:v", "libvpx", "-deadline", "realtime", "-cpu-used", "8",
		"-b:v", s.cfg.VideoBitrate, "-maxrate", s.cfg.VideoBitrate, "-bufsize", "2800k",
		"-f", "rtp", "-payload_type", "96", fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", videoAddr.Port),
		"-map", "0:a:0?", "-vn",
		"-c:a", "libopus", "-b:a", s.cfg.AudioBitrate, "-ar", "48000", "-ac", "2",
		"-f", "rtp", "-payload_type", "111", fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", audioAddr.Port),
	}

	cmd := exec.Command("ffmpeg", cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	s.cfg.LogFn("[media] ffmpeg started pid=%d", cmd.Process.Pid)

	errCh := make(chan error, 1)
	go func() { errCh <- cmd.Wait() }()

	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.readRTP(readCtx, videoConn, s.video, "video")
	go s.readRTP(readCtx, audioConn, s.audio, "audio")

	select {
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return ctx.Err()
	case err := <-errCh:
		cancel()
		return err
	}
}

func (s *Source) readRTP(ctx context.Context, conn *net.UDPConn, track *webrtc.TrackLocalStaticRTP, label string) {
	if track == nil {
		return
	}
	buf := make([]byte, 64*1024)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
			return
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue
		}
		if err := track.WriteRTP(pkt); err != nil {
			s.cfg.LogFn("[media] %s WriteRTP: %v", label, err)
			return
		}
	}
}

func listenRTP(port int) (*net.UDPConn, error) {
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	return net.ListenUDP("udp", addr)
}

func BuildDefaultStreamURL() string {
	if v := strings.TrimSpace(os.Getenv("STREAM_URL")); v != "" {
		return v
	}
	return "https://hlspackager.akamaized.net/live/DB/IRAN_INTERNATIONAL/HLS/IRAN_INTERNATIONAL.m3u8"
}

func EnvInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func InstallSignalHandler(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() { <-ch; cancel() }()
}
