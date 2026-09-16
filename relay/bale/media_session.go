package bale

import (
	"context"
	"fmt"
	"sync"

	"bale-live-call/media"
	"bale-live-call/relay/common"
	"bale-live-call/relay/livekit"
	"github.com/pion/webrtc/v4"
)

type MediaSessionConfig struct {
	WSURL        string
	RoomToken    string
	Origin       string
	LogFn        func(string, ...any)
	StreamURL    string
	Width        int
	Height       int
	FPS          int
	VideoBitrate string
	AudioBitrate string
	VideoPort    int
	AudioPort    int
}

type MediaSession struct {
	cfg   MediaSessionConfig
	lk    *livekit.Client
	video *webrtc.TrackLocalStaticRTP
	audio *webrtc.TrackLocalStaticRTP
	media *media.Source
	done  chan struct{}
	once  sync.Once
}

func NewMediaSession(cfg MediaSessionConfig) *MediaSession {
	if cfg.LogFn == nil {
		cfg.LogFn = func(string, ...any) {}
	}
	return &MediaSession{cfg: cfg, done: make(chan struct{})}
}

func (s *MediaSession) Done() <-chan struct{} { return s.done }

func (s *MediaSession) Start(ctx context.Context) error {
	video, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000},
		"iranintl-video", "iranintl-live",
	)
	if err != nil {
		return fmt.Errorf("create video track: %w", err)
	}
	audio, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"iranintl-audio", "iranintl-live",
	)
	if err != nil {
		return fmt.Errorf("create audio track: %w", err)
	}
	s.video, s.audio = video, audio

	s.media = media.New(media.Config{
		StreamURL: s.cfg.StreamURL,
		Width:     s.cfg.Width, Height: s.cfg.Height, FPS: s.cfg.FPS,
		VideoBitrate: s.cfg.VideoBitrate, AudioBitrate: s.cfg.AudioBitrate,
		VideoPort: s.cfg.VideoPort, AudioPort: s.cfg.AudioPort, LogFn: s.cfg.LogFn,
	}, video, audio)

	s.lk = livekit.NewClient(livekit.Config{
		ServerURL: s.cfg.WSURL, Token: s.cfg.RoomToken, Origin: s.cfg.Origin,
		UserAgent: common.UserAgent, LogFn: s.cfg.LogFn,
	})
	s.lk.OnReady = s.onReady
	s.lk.OnPubConnected = func() {
		s.cfg.LogFn("[media] Bale publisher connected; starting source")
		go func() {
			if err := s.media.Run(ctx); err != nil && ctx.Err() == nil {
				s.cfg.LogFn("[media] source stopped: %v", err)
			}
		}()
	}

	if err := s.lk.Connect(); err != nil {
		return err
	}
	go s.lk.PingLoop()
	go func() {
		if err := s.lk.ReadLoop(); err != nil {
			s.cfg.LogFn("[lk] read loop ended: %v", err)
		}
		s.once.Do(func() { close(s.done) })
	}()
	return nil
}

func (s *MediaSession) onReady() {
	pc := s.lk.PubPC()
	if pc == nil {
		s.cfg.LogFn("[media] publisher PC is nil")
		return
	}

	if _, err := pc.AddTransceiverFromTrack(s.video, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
		s.cfg.LogFn("[media] add video transceiver: %v", err)
		return
	}
	if _, err := pc.AddTransceiverFromTrack(s.audio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
		s.cfg.LogFn("[media] add audio transceiver: %v", err)
		return
	}
	if err := s.lk.SendAddTrack(s.video.ID(), "Iran International", livekit.TrackTypeVideo, livekit.TrackSourceCamera, uint32(s.cfg.Width), uint32(s.cfg.Height)); err != nil {
		s.cfg.LogFn("[media] send video add-track: %v", err)
		return
	}
	if err := s.lk.SendAddTrack(s.audio.ID(), "Iran International Audio", livekit.TrackTypeAudio, livekit.TrackSourceMicrophone, 0, 0); err != nil {
		s.cfg.LogFn("[media] send audio add-track: %v", err)
		return
	}

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		s.cfg.LogFn("[media] create offer: %v", err)
		return
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		s.cfg.LogFn("[media] set local offer: %v", err)
		return
	}
	if err := s.lk.SendOffer(offer.SDP); err != nil {
		s.cfg.LogFn("[media] send offer: %v", err)
		return
	}
	s.cfg.LogFn("[media] publisher offer sent: video=%dx%d fps=%d", s.cfg.Width, s.cfg.Height, s.cfg.FPS)
}

func (s *MediaSession) Close() {
	if s.lk != nil {
		s.lk.Close()
	}
	s.once.Do(func() { close(s.done) })
}
