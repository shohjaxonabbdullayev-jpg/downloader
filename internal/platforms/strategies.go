package platforms

import (
	"os"
	"strings"

	"telegram_bot_downloader/internal/model"
	"telegram_bot_downloader/internal/urlx"
)

type simpleFallbackStrategy struct {
	primary  Engine
	fallback Engine
}

func (s simpleFallbackStrategy) EnginesFor(_ *model.MediaInfo) []Engine {
	return []Engine{s.primary, s.fallback}
}

func (s simpleFallbackStrategy) OptionsMatrix(url string) []Options {
	return defaultRetryOptions(url)
}

type instagramStrategy struct {
	insta Engine
	yt    Engine
	gd    Engine
}

func (s instagramStrategy) EnginesFor(info *model.MediaInfo) []Engine {
	// If detector thinks it's video, prioritize yt-dlp.
	if info != nil && strings.EqualFold(info.Type, "video") {
		return []Engine{s.yt, s.gd}
	}
	// Otherwise, try instaloader images first.
	return []Engine{s.insta, s.gd, s.yt}
}

func (s instagramStrategy) OptionsMatrix(url string) []Options {
	return defaultRetryOptions(url)
}

func defaultRetryOptions(url string) []Options {
	plat := urlx.PlatformFromURL(url)
	cookies := cookiesFileForPlatform(plat)

	opts := []Options{
		// normal
		{MaxHeight: "2160"},
	}

	// Only add cookie-based attempts if the cookie file exists.
	if cookies != "" && fileExists(cookies) {
		opts = append(opts,
			Options{CookiesFile: cookies, MaxHeight: "2160"},
			Options{CookiesFile: cookies, UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36", MaxHeight: "2160"},
		)
	}

	return opts
}

func cookiesFileForPlatform(platform string) string {
	switch platform {
	case "instagram":
		return "instagram.txt"
	case "twitter":
		return "twitter.txt"
	case "facebook":
		return "facebook.txt"
	case "pinterest":
		return "pinterest.txt"
	case "youtube":
		return "youtube.txt"
	default:
		return ""
	}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

