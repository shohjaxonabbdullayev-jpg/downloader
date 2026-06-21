package platforms

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

	// Proxy, if set, routes all yt-dlp traffic through it (e.g. a residential
	// proxy). This is the most reliable way to download YouTube from a cloud/
	// datacenter IP without cookies. Empty => direct connection.
	Proxy string
	// YouTubePlayerClient is passed as yt-dlp's youtube:player_client extractor
	// arg. Alternate clients (tv, mweb, web_safari, …) often bypass YouTube's
	// "confirm you're not a bot" check that otherwise forces a login/cookies.
	YouTubePlayerClient string
}

func (e YtDlpEngine) Name() string { return "yt-dlp" }

func (e YtDlpEngine) Download(ctx context.Context, url string, jobDir string, opts Options) (*model.DownloadResult, error) {
	cmd := e.Cmd
	if cmd == "" {
		cmd = "yt-dlp"
	}

	if e.Timeout > 0 {
		var c2 context.CancelFunc
		ctx, c2 = context.WithTimeout(ctx, e.Timeout)
		defer c2()
	}

	out := filepath.Join(jobDir, "%(title).80s_%(id)s.%(ext)s")

	ul := strings.ToLower(url)
	isFB := strings.Contains(ul, "facebook.com") || strings.Contains(ul, "fb.watch")
	isYT := strings.Contains(ul, "youtube.com") || strings.Contains(ul, "youtu.be")

	args := []string{
		// No --quiet: keep stderr in logs when a run fails (Render/Docker).
		"--no-warnings",
		"--no-part",
		// Fail fast on stalled connections instead of hanging the whole job.
		"--socket-timeout", "15",
	}
	// YouTube must NOT expand playlists — a watch?v=...&list=... link would pull
	// the entire list. Everywhere else, keep playlist expansion ON so multi-item
	// carousels (Instagram /p/, multi-image tweets, Facebook albums) download
	// every item instead of just the first.
	if isYT {
		args = append(args, "--no-playlist")
	} else {
		args = append(args, "--yes-playlist")
	}

	// This bot has no login sessions configured; always run anonymously.
	args = append(args, "--no-cookies")

	// Optional proxy (e.g. a residential proxy) — the most reliable way to fetch
	// YouTube from a cloud IP without cookies.
	if p := strings.TrimSpace(e.Proxy); p != "" {
		args = append(args, "--proxy", p)
	}
	// YouTube on datacenter IPs throws "Sign in to confirm you're not a bot".
	// Using alternate player clients usually avoids that without cookies.
	if isYT && strings.TrimSpace(e.YouTubePlayerClient) != "" {
		args = append(args, "--extractor-args", "youtube:player_client="+strings.TrimSpace(e.YouTubePlayerClient))
	}

	if opts.UserAgent != "" {
		args = append(args, "--user-agent", opts.UserAgent)
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

	// Image / carousel posts (e.g. Instagram /p/): the video-only selectors below
	// would skip non-video items, and carousels need every item. Do a permissive
	// pass first that accepts photos and grabs all entries.
	isMedia := strings.EqualFold(opts.MediaType, "image") || strings.EqualFold(opts.MediaType, "carousel")
	if isMedia && !isYT {
		mediaArgs := append([]string{}, args...)
		mediaArgs = append(mediaArgs, "-f", "best", "--print", "after_move:filepath", "-o", out, "--", url)
		files, err := runYtDlpAndCollectFiles(ctx, cmd, mediaArgs, jobDir)
		if err == nil && len(files) > 0 {
			return &model.DownloadResult{Files: files, Size: totalSize(files)}, nil
		}
		// An image/carousel post gains nothing from the video-only cascade below
		// (it would just waste time), so stop here and let the next engine try.
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("yt-dlp found no downloadable media (login may be required)")
	}

	// Speed-first: prefer an already-merged MP4 within the height cap to avoid
	// fetching needlessly large renditions (and any ffmpeg merge). This is the
	// format that succeeds first for almost every link, so bounding it by height
	// is the single biggest latency win.
	fastArgs := append([]string{}, args...)
	fastFormat := fmt.Sprintf("best[ext=mp4][height<=%s]/best[height<=%s]/best[ext=mp4]/best", maxH, maxH)
	switch {
	case isYT:
		// YouTube serves separate video/audio streams and ranks AV1/VP9 highest,
		// but Telegram only reliably plays H.264 (avc1)+AAC — anything else shows
		// audio over a frozen frame. Force avc1 video + m4a audio, height-bounded,
		// and fall back to the 360p avc1 progressive before ever touching AV1.
		fastFormat = fmt.Sprintf("bestvideo[vcodec^=avc1][height<=%s]+bestaudio[ext=m4a]/best[vcodec^=avc1][height<=%s]/best[ext=mp4][height<=%s]/best[height<=%s]/best", maxH, maxH, maxH, maxH)
	case isFB:
		// Facebook playlists / reels: not every entry is mp4; prefer a progressive
		// mp4 first (no merge), then fall back to merged streams.
		fastFormat = fmt.Sprintf("best[ext=mp4][height<=%s]/bestvideo[height<=%s]+bestaudio/best[height<=%s]/best", maxH, maxH, maxH)
	}
	if opts.MaxFilesize != "" {
		// Best effort to stay under the Telegram bot limit (approx).
		fastFormat = fmt.Sprintf("best[ext=mp4][height<=%s][filesize<%s]/best[height<=%s][filesize<%s]/best[ext=mp4][filesize<%s]/best", maxH, opts.MaxFilesize, maxH, opts.MaxFilesize, opts.MaxFilesize)
		switch {
		case isYT:
			fastFormat = fmt.Sprintf("bestvideo[vcodec^=avc1][height<=%s][filesize<%s]+bestaudio[ext=m4a]/best[vcodec^=avc1][height<=%s][filesize<%s]/best[ext=mp4][height<=%s][filesize<%s]/best[filesize<%s]/best", maxH, opts.MaxFilesize, maxH, opts.MaxFilesize, maxH, opts.MaxFilesize, opts.MaxFilesize)
		case isFB:
			fastFormat = fmt.Sprintf("best[ext=mp4][height<=%s][filesize<%s]/bestvideo[height<=%s]+bestaudio/best[ext=mp4][filesize<%s]/best[ext=webm][filesize<%s]/best[filesize<%s]/best", maxH, opts.MaxFilesize, maxH, opts.MaxFilesize, opts.MaxFilesize, opts.MaxFilesize)
		}
	}
	fastArgs = append(fastArgs, "-f", fastFormat)
	if isYT {
		// YouTube's fast path is a merge, so force a streamable mp4 container.
		fastArgs = append(fastArgs, "--merge-output-format", "mp4", "--postprocessor-args", "ffmpeg:-movflags +faststart")
	}
	fastArgs = append(fastArgs,
		"--print", "after_move:filepath",
		"-o", out,
		"--", url,
	)

	files, err := runYtDlpAndCollectFiles(ctx, cmd, fastArgs, jobDir)
	if err == nil && len(files) > 0 {
		return &model.DownloadResult{Files: files, Size: totalSize(files)}, nil
	}

	// Quality fallback: best MP4 video + best M4A audio (may require merge).
	qualityArgs := append([]string{}, args...)
	qualityFormat := fmt.Sprintf("bestvideo[ext=mp4][height<=%s]+bestaudio[ext=m4a]/best[ext=mp4]/best", maxH)
	if isFB {
		qualityFormat = fmt.Sprintf("bestvideo[ext=mp4][height<=%s]+bestaudio[ext=m4a]/bestvideo[height<=%s]+bestaudio/bestvideo+bestaudio/best[ext=mp4]/best[ext=webm]/best", maxH, maxH)
	}
	if opts.MaxFilesize != "" {
		qualityFormat = fmt.Sprintf("bestvideo[ext=mp4][height<=%s][filesize<%s]+bestaudio[ext=m4a][filesize<%s]/best[ext=mp4][filesize<%s]/best[filesize<%s]/best", maxH, opts.MaxFilesize, opts.MaxFilesize, opts.MaxFilesize, opts.MaxFilesize)
		if isFB {
			qualityFormat = fmt.Sprintf("bestvideo[ext=mp4][height<=%s][filesize<%s]+bestaudio[ext=m4a][filesize<%s]/bestvideo[height<=%s]+bestaudio/best[ext=mp4][filesize<%s]/best[ext=webm][filesize<%s]/best", maxH, opts.MaxFilesize, opts.MaxFilesize, maxH, opts.MaxFilesize, opts.MaxFilesize)
		}
	}
	qualityArgs = append(qualityArgs,
		"-f", qualityFormat,
		"--merge-output-format", "mp4",
		// Stream-copy remux only (fast). Codec normalization is left to the
		// compat fallback below so the common case never pays a re-encode.
		"--postprocessor-args", "ffmpeg:-movflags +faststart",
		"--print", "after_move:filepath",
		"-o", out,
		"--", url,
	)
	files, err = runYtDlpAndCollectFiles(ctx, cmd, qualityArgs, jobDir)
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

// GalleryDlEngine is used for Facebook multi-image carousels; yt-dlp’s Facebook extractor only handles Video nodes, not photos.
type GalleryDlEngine struct {
	Cmd string
}

func (e GalleryDlEngine) Name() string { return "gallery-dl" }

func (e GalleryDlEngine) Download(ctx context.Context, url string, jobDir string, opts Options) (*model.DownloadResult, error) {
	cmd := e.Cmd
	if cmd == "" {
		cmd = "gallery-dl"
	}
	// No login sessions: gallery-dl runs anonymously (public content only).
	args := []string{"-d", jobDir, "--", url}

	res, err := execx.Run(ctx, cmd, args...)
	if err != nil {
		out := strings.TrimSpace(res.Output)
		if out != "" {
			return nil, fmt.Errorf("%w: %s", err, out)
		}
		return nil, err
	}
	files := allFiles(jobDir)
	if len(files) == 0 {
		return nil, fmt.Errorf("gallery-dl produced no files")
	}
	return &model.DownloadResult{Files: files, Size: totalSize(files)}, nil
}

type InstaloaderImagesEngine struct {
	// Python is the python executable to use (e.g. "python3" or "python").
	// If empty, the engine will try "python3" and then "python".
	Python string
}

func (e InstaloaderImagesEngine) Name() string { return "instaloader(images)" }

func (e InstaloaderImagesEngine) Download(ctx context.Context, url string, jobDir string, opts Options) (*model.DownloadResult, error) {
	shortcode := extractInstagramShortcode(url)
	if shortcode == "" {
		return nil, fmt.Errorf("could not extract instagram shortcode")
	}

	py := strings.TrimSpace(e.Python)
	candidates := []string{}
	if py != "" {
		candidates = append(candidates, py)
	} else {
		// Dockerfile venv (Render/Railway Docker deploys): instaloader lives here.
		candidates = append(candidates,
			"/opt/yt/bin/python3",
			"/opt/yt/bin/python",
			"/usr/bin/python3",
			"/usr/local/bin/python3",
			"python3",
			"python",
			// "py" is the Windows Python launcher.
			"py",
		)
	}

	// Filter to only candidates that exist on this host.
	var resolved []string
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil && p != "" {
			resolved = append(resolved, c)
		}
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("%w: python not available; cannot use instaloader", ErrEngineUnavailable)
	}

	// Use Instaloader **Python library** (not CLI) and download images only.
	// This avoids CLI-specific behavior differences across platforms.
	code := `
import os, sys
try:
    import instaloader
    from instaloader import Post
except Exception as e:
    print("IMPORT_ERROR:", repr(e))
    raise

shortcode = sys.argv[1]
target_dir = sys.argv[2]

L = instaloader.Instaloader(
    dirname_pattern=target_dir,
    filename_pattern="{shortcode}_{date_utc}_UTC",
    download_videos=False,
    download_video_thumbnails=False,
    download_geotags=False,
    download_comments=False,
    save_metadata=False,
    post_metadata_txt_pattern="",
    quiet=True,
)

try:
    post = Post.from_shortcode(L.context, shortcode)
    L.download_post(post, target=".")
    print("OK")
except Exception as e:
    print("RUNTIME_ERROR:", repr(e))
    raise
`

	var lastErr error
	var lastOut string
	for _, candidate := range resolved {
		res, err := execx.Run(ctx, candidate, "-c", code, shortcode, jobDir)
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		lastOut = strings.TrimSpace(res.Output)
	}
	if lastErr != nil {
		// If the Python env exists but the module isn't installed, don't waste retries;
		// mark this engine as unavailable so the pipeline can fall back immediately.
		if strings.Contains(lastOut, "No module named 'instaloader'") ||
			strings.Contains(lastOut, "No module named \"instaloader\"") ||
			strings.Contains(lastOut, "IMPORT_ERROR:") && strings.Contains(lastOut, "ModuleNotFoundError") {
			return nil, fmt.Errorf("%w: python module 'instaloader' is not installed (%s)", ErrEngineUnavailable, lastOut)
		}
		if lastOut != "" {
			return nil, fmt.Errorf("%w: %s", lastErr, lastOut)
		}
		return nil, lastErr
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

func filePathWithinDir(file, dir string) bool {
	cf, err1 := filepath.Abs(filepath.Clean(file))
	cd, err2 := filepath.Abs(filepath.Clean(dir))
	if err1 != nil || err2 != nil {
		return strings.HasPrefix(strings.ToLower(file), strings.ToLower(dir))
	}
	rel, err := filepath.Rel(cd, cf)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func runYtDlpAndCollectFiles(ctx context.Context, cmd string, args []string, jobDir string) ([]string, error) {
	res, err := execx.Run(ctx, cmd, args...)
	if err != nil {
		out := strings.TrimSpace(res.Output)
		if out != "" {
			return nil, fmt.Errorf("%w: %s", err, out)
		}
		return nil, err
	}
	// With --print after_move:filepath, yt-dlp prints one path per line.
	var files []string
	for _, line := range strings.Split(res.Output, "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		if !filePathWithinDir(p, jobDir) {
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

