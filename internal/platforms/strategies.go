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
	fast   Engine // curl_cffi graphql extractor — fast AND works on datacenter IPs
	native Engine // pure-Go graphql extractor — fast where TLS isn't fingerprinted
	insta  Engine // instaloader fork — image/carousel fallback
	yt     Engine // yt-dlp — reliable but slow fallback
}

func (s instagramStrategy) EnginesFor(info *model.MediaInfo) []Engine {
	// Single-request graphql extractors first (reels, photos, carousels in ~1s):
	//   - fast (curl_cffi): browser TLS, the only fingerprint Instagram serves from
	//     a flagged datacenter IP; the production fast path.
	//   - native (pure Go): no subprocess, wins on residential/unflagged IPs; a
	//     no-cost fallback since it never runs on the deploy (fast succeeds first).
	// Both fall through on failure (e.g. Instagram rotating its doc_id).
	if info != nil && strings.EqualFold(info.Type, "video") {
		return []Engine{s.fast, s.native, s.yt}
	}
	// Photos / carousels: then Instaloader (fetches IG photos without cookies where
	// it works), then yt-dlp for the case a /p/ URL is actually a video.
	return []Engine{s.fast, s.native, s.insta, s.yt}
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

