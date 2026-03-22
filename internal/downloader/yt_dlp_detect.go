package downloader

import (
	"context"
	"encoding/json"
	"strings"

	"telegram_bot_downloader/internal/execx"
	"telegram_bot_downloader/internal/model"
	"telegram_bot_downloader/internal/platforms"
)

// YtDlpDetector uses "yt-dlp --dump-json <url>" to extract metadata before downloading.
type YtDlpDetector struct {
	Cmd string // default "yt-dlp"
}

type ytDump struct {
	Extractor string `json:"extractor"`
	Title     string `json:"title"`
	Duration  float64 `json:"duration"`
	Filesize  int64  `json:"filesize"`
	FilesizeA int64  `json:"filesize_approx"`
	WebpageURL string `json:"webpage_url"`
	Uploader  string `json:"uploader"`
	Thumbnail string `json:"thumbnail"`

	// Posts/carousels/collections may appear as playlist-like structures.
	Entries []json.RawMessage `json:"entries"`
	Type    string            `json:"_type"`
}

func (d YtDlpDetector) Detect(ctx context.Context, url string) (*MediaInfo, error) {
	cmd := d.Cmd
	if cmd == "" {
		cmd = "yt-dlp"
	}

	args := []string{"--no-warnings", "--dump-json"}
	if ck := platforms.CookiesPathForURL(url); ck != "" {
		args = append(args, "--cookies", ck)
	} else {
		args = append(args, "--no-cookies")
	}
	args = append(args, "--", url)
	res, err := execx.Run(ctx, cmd, args...)
	if err != nil {
		// Keep error but return a best-effort typed error if possible.
		msg := strings.ToLower(res.Output)
		switch {
		case strings.Contains(msg, "private") || strings.Contains(msg, "login required") || strings.Contains(msg, "requires authentication"):
			return nil, ErrPrivate
		case strings.Contains(msg, "not found") || strings.Contains(msg, "404"):
			return nil, ErrNotFound
		default:
			return nil, err
		}
	}

	var dump ytDump
	if jerr := json.Unmarshal([]byte(res.Output), &dump); jerr != nil {
		return nil, jerr
	}

	platform := dump.Extractor
	if platform == "" {
		platform = PlatformFromURL(url)
	}

	typ := "unknown"
	if len(dump.Entries) > 0 || strings.Contains(strings.ToLower(dump.Type), "playlist") {
		typ = "carousel"
	} else if dump.Duration > 0 {
		typ = "video"
	} else {
		// Many image posts show duration=0.
		typ = "image"
	}

	size := dump.Filesize
	if size <= 0 {
		size = dump.FilesizeA
	}

	title := dump.Title
	if title == "" {
		title = "media"
	}

	web := dump.WebpageURL
	if web == "" {
		web = url
	}

	return (*MediaInfo)(&model.MediaInfo{
		Platform:  strings.ToLower(platform),
		Type:      typ,
		Duration:  int(dump.Duration + 0.5),
		Size:      size,
		Title:     title,
		WebpageURL: web,
		Uploader:  dump.Uploader,
		Thumbnail: dump.Thumbnail,
	}), nil
}

