package platforms

import (
	"context"
	"fmt"
	"strings"

	"telegram_bot_downloader/internal/model"
	"telegram_bot_downloader/internal/urlx"
)

const telegramBotMaxBytes = 50 * 1024 * 1024

// YouTubeEngine enforces Telegram bot size constraints and nudges the pipeline into the
// max-filesize fallback attempt when needed.
type YouTubeEngine struct {
	Base            Engine
	MaxTelegramBytes int64
}

func (e YouTubeEngine) Name() string {
	if e.Base == nil {
		return "youtube"
	}
	return "youtube/" + e.Base.Name()
}

func (e YouTubeEngine) Download(ctx context.Context, url string, jobDir string, opts Options) (*model.DownloadResult, error) {
	if e.Base == nil {
		return nil, fmt.Errorf("youtube engine missing base")
	}
	limit := e.MaxTelegramBytes
	if limit <= 0 {
		limit = telegramBotMaxBytes
	}

	res, err := e.Base.Download(ctx, url, jobDir, opts)
	if err != nil {
		return nil, err
	}
	if res == nil || len(res.Files) == 0 {
		return nil, fmt.Errorf("empty result")
	}

	if res.Size <= 0 {
		// compute if engine didn't fill size
		res.Size = totalSize(res.Files)
	}

	if res.Size > limit {
		// Force the pipeline to try the max-filesize attempt next.
		if opts.MaxFilesize == "" {
			return nil, fmt.Errorf("file too large for Telegram; retry with max-filesize")
		}
		return nil, fmt.Errorf("file too large for Telegram")
	}
	return res, nil
}

type youtubeStrategy struct {
	yt Engine
}

func (s youtubeStrategy) EnginesFor(_ *model.MediaInfo) []Engine {
	// YouTube / Shorts: yt-dlp only.
	return []Engine{s.yt}
}

func (s youtubeStrategy) OptionsMatrix(url string) []Options {
	// Anonymous attempts first (--no-cookies); optional youtube.txt retries last.
	// Try Telegram-sized formats first to avoid downloading huge files then failing the size check.
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
	opts := []Options{
		{MaxFilesize: "50M"},
		{UserAgent: ua, MaxFilesize: "50M"},
		{},
		{UserAgent: ua},
	}
	if ck := CookiesPathForURL(url); ck != "" {
		opts = append(opts,
			Options{CookiesFile: ck, MaxFilesize: "50M"},
			Options{CookiesFile: ck, UserAgent: ua, MaxFilesize: "50M"},
			Options{CookiesFile: ck},
			Options{CookiesFile: ck, UserAgent: ua},
		)
	}
	return opts
}

func isYouTube(url string) bool {
	return urlx.PlatformFromURL(strings.ToLower(url)) == "youtube"
}

