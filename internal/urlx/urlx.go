package urlx

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

func HashURL(u string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(u)))
	return hex.EncodeToString(sum[:])
}

func NormalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.Fragment = ""
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String()
}

func PlatformFromURL(u string) string {
	l := strings.ToLower(u)
	switch {
	case strings.Contains(l, "youtube.com") || strings.Contains(l, "youtu.be"):
		return "youtube"
	case strings.Contains(l, "instagram.com"):
		return "instagram"
	case strings.Contains(l, "tiktok.com"):
		return "tiktok"
	case strings.Contains(l, "twitter.com") || strings.Contains(l, "x.com"):
		return "twitter"
	case strings.Contains(l, "facebook.com") || strings.Contains(l, "fb.watch"):
		return "facebook"
	case strings.Contains(l, "pinterest.com") || strings.Contains(l, "pin.it"):
		return "pinterest"
	default:
		return "unknown"
	}
}

