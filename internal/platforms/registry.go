package platforms

import (
	"os"
	"strings"

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
		// Height cap (env MAX_HEIGHT, default 1080). Smaller = faster download AND
		// upload; 1080 keeps horizontal videos crisp. Set MAX_HEIGHT=720 to trade
		// quality for speed.
		MaxHeight:           maxHeight(),
		ConcurrentFragments: 16,
		// yt-dlp: YouTube and others throttle http chunk sizes >10M (FAQ).
		HTTPChunkSize:   "10M",
		Retries:         2,
		FragmentRetries: 2,
		// No per-attempt timeout: a slow-but-progressing datacenter download must
		// be allowed to finish. Stalls are caught by --socket-timeout, and the
		// whole job is still bounded by the 5-minute context in main.go.
		//
		// Browser impersonation (curl_cffi) is the strongest way to fetch public
		// media from a flagged datacenter IP without cookies. Auto-detected so it
		// self-disables when curl_cffi isn't installed.
		Impersonate: detectImpersonateTarget("yt-dlp"),
	}
	// INSTALOADER_PYTHON lets a deployment point at a specific Python that has
	// instaloader installed (e.g. a local venv). Empty => the engine's built-in
	// candidate list, which includes the Docker image's /opt/yt venv.
	ig := InstaloaderImagesEngine{Python: strings.TrimSpace(os.Getenv("INSTALOADER_PYTHON"))}

	return Registry{
		// Instagram: curl_cffi graphql extractor first (fast + works on datacenter
		// IPs), then the pure-Go extractor, Instaloader, and yt-dlp as fallbacks.
		Instagram: instagramStrategy{fast: FastInstagramEngine{}, native: NativeInstagramEngine{}, insta: ig, yt: yt},
		// YouTube downloading is removed — it can't be fetched from a datacenter IP
		// without a proxy/cookies, and no free workaround is reliable. main.go
		// replies "not supported" for YouTube links; this no-engine strategy is a
		// safety net for any YouTube URL form that slips past that check.
		YouTube: noEngineStrategy{},
		// gallery-dl is never used (it login-redirects on these platforms and was
		// causing media download errors). Instaloader is Instagram-specific, so
		// every other platform runs on yt-dlp alone — for both video and images.
		TikTok:    ytOnlyStrategy{yt: yt},
		Twitter:   ytOnlyStrategy{yt: yt},
		Pinterest: ytOnlyStrategy{yt: yt},
		Facebook:  ytOnlyStrategy{yt: yt},
		Default:   ytOnlyStrategy{yt: yt},
	}
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

