package platforms

import (
	"context"

	"telegram_bot_downloader/internal/model"
)

type Engine interface {
	Name() string
	Download(ctx context.Context, url string, jobDir string, opts Options) (*model.DownloadResult, error)
}

type Options struct {
	CookiesFile string
	UserAgent   string
	MaxHeight   string
	MaxFilesize string // e.g. "50M"
	// NoCookies forces anonymous yt-dlp/gallery-dl behavior (no cookie files).
	NoCookies bool
}

type Strategy interface {
	EnginesFor(info *model.MediaInfo) []Engine
	OptionsMatrix(url string) []Options
}

