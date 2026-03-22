package platforms

import (
	"os"
	"strings"

	"telegram_bot_downloader/internal/urlx"
)

func cookiesPathForPlatform(platform string) string {
	switch strings.ToLower(platform) {
	case "instagram":
		return "instagram.txt"
	case "twitter":
		return "twitter.txt"
	case "facebook":
		return "facebook.txt"
	case "pinterest":
		return "pinterest.txt"
	default:
		return ""
	}
}

// CookiesPathForURL returns a cookie file path when a non-empty Netscape-format
// file exists for the URL's platform (see yt-dlp --cookies). Otherwise "".
func CookiesPathForURL(rawURL string) string {
	p := cookiesPathForPlatform(urlx.PlatformFromURL(strings.ToLower(rawURL)))
	if p == "" {
		return ""
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() || st.Size() == 0 {
		return ""
	}
	return p
}
