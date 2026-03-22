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
		// yt-dlp: YouTube and others throttle http chunk sizes >10M (FAQ).
		HTTPChunkSize: "10M",
		Retries:             2,
		FragmentRetries:     2,
	}
	ig := InstaloaderImagesEngine{}
	gd := GalleryDlEngine{}

	return Registry{
		Instagram: instagramStrategy{insta: ig, yt: yt},
		YouTube:   noEngineStrategy{},
		TikTok:    ytOnlyStrategy{yt: yt},
		Twitter:   ytOnlyStrategy{yt: yt},
		Facebook:  facebookStrategy{yt: yt, gd: gd},
		Pinterest: ytOnlyStrategy{yt: yt},
		Default:   ytOnlyStrategy{yt: yt},
	}
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

