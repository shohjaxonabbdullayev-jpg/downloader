package platforms

import (
	"strings"

	"telegram_bot_downloader/internal/model"
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
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
	opts := []Options{
		{MaxHeight: "2160"},
		{UserAgent: ua, MaxHeight: "2160"},
	}
	if ck := CookiesPathForURL(url); ck != "" {
		opts = append(opts,
			Options{CookiesFile: ck, MaxHeight: "2160"},
			Options{CookiesFile: ck, UserAgent: ua, MaxHeight: "2160"},
		)
	}
	return opts
}

