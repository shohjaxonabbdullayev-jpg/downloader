package platforms

import (
	"strings"

	"telegram_bot_downloader/internal/model"
)

// noEngineStrategy disables downloads for a platform (e.g. YouTube removed from the bot).
type noEngineStrategy struct{}

func (noEngineStrategy) EnginesFor(*model.MediaInfo) []Engine { return nil }

func (noEngineStrategy) OptionsMatrix(string) []Options { return nil }

type ytOnlyStrategy struct {
	yt Engine
}

func (s ytOnlyStrategy) EnginesFor(_ *model.MediaInfo) []Engine {
	return []Engine{s.yt}
}

func (s ytOnlyStrategy) OptionsMatrix(url string) []Options {
	return defaultRetryOptions(url)
}

type instagramStrategy struct {
	insta Engine
	yt    Engine
}

func (s instagramStrategy) EnginesFor(info *model.MediaInfo) []Engine {
	if info != nil && strings.EqualFold(info.Type, "video") {
		return []Engine{s.yt}
	}
	// Photos / carousels: Instaloader first, then yt-dlp if needed.
	return []Engine{s.insta, s.yt}
}

func (s instagramStrategy) OptionsMatrix(url string) []Options {
	return defaultRetryOptions(url)
}

type facebookStrategy struct {
	yt Engine
	gd Engine
}

// Photo / multi-image carousels: gallery-dl first (yt-dlp skips non-Video attachments).
// Obvious video URLs: yt-dlp first.
func (s facebookStrategy) EnginesFor(info *model.MediaInfo) []Engine {
	if info != nil && strings.EqualFold(info.Type, "video") {
		return []Engine{s.yt, s.gd}
	}
	return []Engine{s.gd, s.yt}
}

func (s facebookStrategy) OptionsMatrix(url string) []Options {
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

