package downloader

import (
	"telegram_bot_downloader/internal/urlx"
)

func HashURL(u string) string {
	return urlx.HashURL(u)
}

func NormalizeURL(raw string) string {
	return urlx.NormalizeURL(raw)
}

func PlatformFromURL(u string) string {
	return urlx.PlatformFromURL(u)
}

