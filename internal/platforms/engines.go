package platforms

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"telegram_bot_downloader/internal/execx"
	"telegram_bot_downloader/internal/model"
)

type YtDlpEngine struct {
	Cmd                 string
	MaxHeight           string
	CompatMP4Fallback   bool
	Timeout             time.Duration
	ConcurrentFragments int
	HTTPChunkSize       string
	Retries             int
	FragmentRetries     int
}

func (e YtDlpEngine) Name() string { return "yt-dlp" }

func (e YtDlpEngine) Download(ctx context.Context, url string, jobDir string, opts Options) (*model.DownloadResult, error) {
	cmd := e.Cmd
	if cmd == "" {
		cmd = "yt-dlp"
	}

	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := filepath.Join(jobDir, "%(title).80s_%(id)s.%(ext)s")
	args := []string{
		"--quiet",
		"--no-warnings",
		"--yes-playlist",
		"--no-part",
	}

	if opts.UserAgent != "" {
		args = append(args, "--user-agent", opts.UserAgent)
	}
	if opts.CookiesFile != "" {
		args = append(args, "--cookies", opts.CookiesFile)
	}
	if opts.MaxFilesize != "" {
		args = append(args, "--max-filesize", opts.MaxFilesize)
	}

	cf := e.ConcurrentFragments
	if cf <= 0 {
		cf = 5
	}
	args = append(args, "--concurrent-fragments", fmt.Sprintf("%d", cf))

	httpChunk := e.HTTPChunkSize
	if httpChunk == "" {
		httpChunk = "10M"
	}
	args = append(args, "--http-chunk-size", httpChunk)

	retries := e.Retries
	if retries <= 0 {
		retries = 3
	}
	fragRetries := e.FragmentRetries
	if fragRetries <= 0 {
		fragRetries = 3
	}
	args = append(args,
		"--retries", fmt.Sprintf("%d", retries),
		"--fragment-retries", fmt.Sprintf("%d", fragRetries),
	)

	maxH := e.MaxHeight
	if opts.MaxHeight != "" {
		maxH = opts.MaxHeight
	}
	if maxH == "" {
		maxH = "1080"
	}

	// Quality-first:
	// Prefer best MP4 video + best M4A audio (may require merge), with a fallback
	// to already merged MP4 if needed.
	qualityArgs := append([]string{}, args...)
	qualityFormat := fmt.Sprintf("bestvideo[ext=mp4][height<=%s]+bestaudio[ext=m4a]/best[ext=mp4]/best", maxH)
	if opts.MaxFilesize != "" {
		qualityFormat = fmt.Sprintf("bestvideo[ext=mp4][height<=%s][filesize<%s]+bestaudio[ext=m4a][filesize<%s]/best[ext=mp4][filesize<%s]/best[filesize<%s]/best", maxH, opts.MaxFilesize, opts.MaxFilesize, opts.MaxFilesize, opts.MaxFilesize)
	}
	qualityArgs = append(qualityArgs,
		"-f", qualityFormat,
		"--merge-output-format", "mp4",
		"--postprocessor-args", "ffmpeg:-movflags +faststart -pix_fmt yuv420p",
		"--print", "after_move:filepath",
		"-o", out,
		"--", url,
	)
	files, err := runYtDlpAndCollectFiles(ctx, cmd, qualityArgs, jobDir)
	if err == nil && len(files) > 0 {
		return &model.DownloadResult{Files: files, Size: totalSize(files)}, nil
	}

	// Speed fallback: Prefer already merged MP4 to avoid ffmpeg merge.
	fastArgs := append([]string{}, args...)
	fastFormat := "best[ext=mp4]/best"
	if opts.MaxFilesize != "" {
		// Best effort to stay under Telegram bot limit (approx).
		fastFormat = fmt.Sprintf("best[ext=mp4][filesize<%s]/best[filesize<%s]/best", opts.MaxFilesize, opts.MaxFilesize)
	}
	fastArgs = append(fastArgs,
		"-f", fastFormat,
		"--print", "after_move:filepath",
		"-o", out,
		"--", url,
	)

	files, err = runYtDlpAndCollectFiles(ctx, cmd, fastArgs, jobDir)
	if err == nil && len(files) > 0 {
		return &model.DownloadResult{Files: files, Size: totalSize(files)}, nil
	}

	// Fallback to compatibility selection (may require merge).
	if e.CompatMP4Fallback {
		compatArgs := append([]string{}, args...)
		compatFormat := fmt.Sprintf("bv*[vcodec^=avc1][height<=%s]+ba[acodec^=mp4a]/b[ext=mp4]/b", maxH)
		if opts.MaxFilesize != "" {
			compatFormat = fmt.Sprintf("bv*[vcodec^=avc1][height<=%s][filesize<%s]+ba[acodec^=mp4a][filesize<%s]/b[ext=mp4][filesize<%s]/b", maxH, opts.MaxFilesize, opts.MaxFilesize, opts.MaxFilesize)
		}
		compatArgs = append(compatArgs,
			"-f", compatFormat,
			"--merge-output-format", "mp4",
			"--postprocessor-args", "ffmpeg:-movflags +faststart -pix_fmt yuv420p",
			"--print", "after_move:filepath",
			"-o", out,
			"--", url,
		)
		files, err2 := runYtDlpAndCollectFiles(ctx, cmd, compatArgs, jobDir)
		if err2 == nil && len(files) > 0 {
			return &model.DownloadResult{Files: files, Size: totalSize(files)}, nil
		}
		if err2 != nil {
			return nil, err2
		}
	}

	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("yt-dlp produced no files")
}

type GalleryDlEngine struct {
	Cmd string
}

func (e GalleryDlEngine) Name() string { return "gallery-dl" }

func (e GalleryDlEngine) Download(ctx context.Context, url string, jobDir string, opts Options) (*model.DownloadResult, error) {
	cmd := e.Cmd
	if cmd == "" {
		cmd = "gallery-dl"
	}
	_, err := execx.Run(ctx, cmd, "-d", jobDir, "--", url)
	if err != nil {
		return nil, err
	}
	files := allFiles(jobDir)
	if len(files) == 0 {
		return nil, fmt.Errorf("gallery-dl produced no files")
	}
	return &model.DownloadResult{Files: files, Size: totalSize(files)}, nil
}

type InstaloaderImagesEngine struct {
	Cmd string
}

func (e InstaloaderImagesEngine) Name() string { return "instaloader(images)" }

func (e InstaloaderImagesEngine) Download(ctx context.Context, url string, jobDir string, opts Options) (*model.DownloadResult, error) {
	shortcode := extractInstagramShortcode(url)
	if shortcode == "" {
		return nil, fmt.Errorf("could not extract instagram shortcode")
	}
	cmd := e.Cmd
	if cmd == "" {
		cmd = "instaloader"
	}

	// Only images. Reels/videos should yield no image files.
	_, err := execx.Run(
		ctx,
		cmd,
		"--no-videos",
		"--no-video-thumbnails",
		"--no-metadata-json",
		"--no-captions",
		"--dirname-pattern", jobDir,
		"--filename-pattern", "{shortcode}_{date_utc}_UTC",
		"--", shortcode,
	)
	if err != nil {
		return nil, err
	}
	files := filterImages(allFiles(jobDir))
	if len(files) == 0 {
		return nil, fmt.Errorf("instaloader produced no images")
	}
	return &model.DownloadResult{Files: files, Size: totalSize(files)}, nil
}

func extractInstagramShortcode(link string) string {
	re := regexp.MustCompile(`instagram\.com/(?:p|reel|tv)/([^/?#]+)`)
	m := re.FindStringSubmatch(link)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

func filterImages(files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp":
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

func runYtDlpAndCollectFiles(ctx context.Context, cmd string, args []string, jobDir string) ([]string, error) {
	res, err := execx.Run(ctx, cmd, args...)
	if err != nil {
		return nil, err
	}
	// With --print after_move:filepath, yt-dlp prints one path per line.
	var files []string
	for _, line := range strings.Split(res.Output, "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		// Only accept files that exist and are within jobDir (defense-in-depth).
		if !strings.HasPrefix(strings.ToLower(p), strings.ToLower(jobDir)) {
			continue
		}
		if st, statErr := os.Stat(p); statErr == nil && !st.IsDir() {
			files = append(files, p)
		}
	}
	if len(files) > 0 {
		sort.Strings(files)
		return files, nil
	}
	// Fallback: directory walk (covers cases where --print isn't emitted).
	files = allFiles(jobDir)
	if len(files) == 0 {
		return nil, nil
	}
	return files, nil
}

