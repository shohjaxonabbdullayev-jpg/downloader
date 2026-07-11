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
	// Photos / carousels: Instaloader FIRST — yt-dlp can't extract Instagram
	// photos anonymously (it reports "0 items"), so Instaloader is the only engine
	// that fetches them without cookies. yt-dlp stays as a fallback for the case a
	// /p/ URL is actually a video.
	return []Engine{s.insta, s.yt}
}

func (s instagramStrategy) OptionsMatrix(url string) []Options {
	return defaultRetryOptions(url)
}

// defaultRetryOptions builds the attempt matrix. A single attempt: yt-dlp already
// does its own internal retries, and a second app-level attempt mostly just
// doubled the time for engines that ignore these options (Instaloader ran twice
// on every miss). No cookie attempts — the bot has no login sessions.
func defaultRetryOptions(_ string) []Options {
	return []Options{
		{MaxHeight: maxHeight()},
	}
}

