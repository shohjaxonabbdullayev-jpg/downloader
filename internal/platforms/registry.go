package platforms

import (
	"os"
	"strings"
	"time"

	"telegram_bot_downloader/internal/model"
	"telegram_bot_downloader/internal/urlx"
)

type Registry struct {
	Instagram  Strategy
	YouTube    Strategy
	TikTok     Strategy
	Twitter    Strategy
	Facebook   Strategy
	Pinterest  Strategy
	Default    Strategy
}

func DefaultRegistry() Registry {
	yt := YtDlpEngine{
		CompatMP4Fallback: true,
		// Height cap (env MAX_HEIGHT, default 720): smaller files download AND
		// upload to Telegram much faster. Raise to 1080 if quality matters more.
		MaxHeight:           maxHeight(),
		ConcurrentFragments: 16,
		// yt-dlp: YouTube and others throttle http chunk sizes >10M (FAQ).
		HTTPChunkSize:   "10M",
		Retries:         2,
		FragmentRetries: 2,
		// Bound each individual yt-dlp attempt so one stuck run can't consume the
		// whole job budget before the fallbacks get a chance.
		Timeout: 90 * time.Second,
		// YouTube anti-bot mitigations, tunable via env without a code redeploy.
		Proxy:               strings.TrimSpace(os.Getenv("YTDLP_PROXY")),
		YouTubePlayerClient: youtubePlayerClient(),
	}
	// INSTALOADER_PYTHON lets a deployment point at a specific Python that has
	// instaloader installed (e.g. a local venv). Empty => the engine's built-in
	// candidate list, which includes the Docker image's /opt/yt venv.
	ig := InstaloaderImagesEngine{Python: strings.TrimSpace(os.Getenv("INSTALOADER_PYTHON"))}
	gd := GalleryDlEngine{}

	return Registry{
		Instagram: instagramStrategy{insta: ig, yt: yt},
		YouTube:   ytOnlyStrategy{yt: yt},
		// Video-first, gallery-dl fallback for photo posts / multi-image tweets.
		TikTok:  engineListStrategy{video: []Engine{yt, gd}, media: []Engine{gd, yt}},
		Twitter: engineListStrategy{video: []Engine{yt, gd}, media: []Engine{gd, yt}},
		// Pinterest is mostly images: gallery-dl first, yt-dlp for the occasional video pin.
		Pinterest: engineListStrategy{video: []Engine{gd, yt}, media: []Engine{gd, yt}},
		Facebook:  facebookStrategy{yt: yt, gd: gd},
		Default:   ytOnlyStrategy{yt: yt},
	}
}

// youtubePlayerClient picks the yt-dlp youtube:player_client value. YouTube
// frequently changes which clients work cookie-free from datacenter IPs, so this
// is overridable via the YT_PLAYER_CLIENT env var (comma-separated client list)
// without rebuilding. The default favors clients that tend to skip the
// "confirm you're not a bot" check.
func youtubePlayerClient() string {
	if v := strings.TrimSpace(os.Getenv("YT_PLAYER_CLIENT")); v != "" {
		return v
	}
	return "tv,web_safari,mweb,default"
}

// maxHeight is the video height cap, overridable via MAX_HEIGHT. Default 1080:
// 720 made horizontal videos look soft (especially on laptops). Vertical videos
// fall through to full resolution either way. Set MAX_HEIGHT=720 to trade
// quality for faster downloads/uploads.
func maxHeight() string {
	if v := strings.TrimSpace(os.Getenv("MAX_HEIGHT")); v != "" {
		return v
	}
	return "1080"
}

func (r Registry) StrategyFor(info *model.MediaInfo, url string) Strategy {
	ul := strings.ToLower(url)
	if strings.Contains(ul, "youtube.com") || strings.Contains(ul, "youtu.be") {
		return r.YouTube
	}

	plat := ""
	if info != nil && info.Platform != "" {
		plat = info.Platform
	} else {
		plat = urlx.PlatformFromURL(url)
	}
	plat = strings.ToLower(plat)

	switch {
	case strings.Contains(plat, "instagram"):
		return r.Instagram
	case strings.Contains(plat, "youtube"):
		return r.YouTube
	case strings.Contains(plat, "tiktok"):
		return r.TikTok
	case strings.Contains(plat, "twitter") || strings.Contains(plat, "x"):
		return r.Twitter
	case strings.Contains(plat, "facebook"):
		return r.Facebook
	case strings.Contains(plat, "pinterest"):
		return r.Pinterest
	default:
		return r.Default
	}
}

