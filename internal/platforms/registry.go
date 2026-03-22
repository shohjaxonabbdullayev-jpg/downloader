package platforms

import (
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
		CompatMP4Fallback:   true,
		MaxHeight:           "2160",
		ConcurrentFragments: 16,
		// YouTube throttles http chunk sizes >10M (yt-dlp FAQ).
		HTTPChunkSize: "10M",
		Retries:             2,
		FragmentRetries:     2,
	}
	ig := InstaloaderImagesEngine{}

	ytyt := YouTubeEngine{Base: yt, MaxTelegramBytes: 50 * 1024 * 1024}

	return Registry{
		Instagram: instagramStrategy{insta: ig, yt: yt},
		YouTube:   youtubeStrategy{yt: ytyt},
		TikTok:    ytOnlyStrategy{yt: yt},
		Twitter:   ytOnlyStrategy{yt: yt},
		Facebook:  ytOnlyStrategy{yt: yt},
		Pinterest: ytOnlyStrategy{yt: yt},
		Default:   ytOnlyStrategy{yt: yt},
	}
}

func (r Registry) StrategyFor(info *model.MediaInfo, url string) Strategy {
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

