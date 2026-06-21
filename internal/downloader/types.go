package downloader

import (
	"context"

	"telegram_bot_downloader/internal/model"
)

type Downloader interface {
	Detect(ctx context.Context, url string) (*MediaInfo, error)
	Download(ctx context.Context, url string, jobDir string) (*DownloadResult, error)
}

// Aliases kept in downloader package to match requested API shape.
type MediaInfo = model.MediaInfo
type DownloadResult = model.DownloadResult

