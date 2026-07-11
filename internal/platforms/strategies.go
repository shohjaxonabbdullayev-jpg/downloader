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
	native Engine // native graphql extractor — fast, handles video + images
	insta  Engine // instaloader fork — image/carousel fallback
	yt     Engine // yt-dlp — video fallback
}

func (s instagramStrategy) EnginesFor(info *model.MediaInfo) []Engine {
	// Native graphql extractor first for everything (a single request that returns
	// reels, photos, and carousels in ~0.5s). It falls through on any failure —
	// e.g. Instagram rotating its doc_id — to the proven engines:
	if info != nil && strings.EqualFold(info.Type, "video") {
		return []Engine{s.native, s.yt}
	}
	// Photos / carousels: after native, Instaloader (the only engine that fetches
	// IG photos without cookies; yt-dlp can't — it reports "0 items"), then yt-dlp
	// for the case a /p/ URL is actually a video.
	return []Engine{s.native, s.insta, s.yt}
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

