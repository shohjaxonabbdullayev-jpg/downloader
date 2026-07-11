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

	// Impersonate, if non-empty (e.g. "chrome"), makes yt-dlp present a real
	// browser TLS/JA3 fingerprint via curl_cffi (--impersonate). This is the
	// single most effective way to fetch public media from a flagged datacenter
	// IP without cookies — it defeats the "you look like a bot" detection that
	// otherwise blocks anonymous requests. Empty => no impersonation (a browser
	// User-Agent is still sent as a weaker fallback). Detected at startup via
	// detectImpersonateTarget so it self-disables when curl_cffi isn't installed.
	Impersonate string
}

// browserUA is a normal desktop Chrome User-Agent. Sent on every yt-dlp request
// (unless we're impersonating, which sets its own matching UA) so platforms that
// reject yt-dlp's default UA still serve us.
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

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

	args := []string{
		// No --quiet: keep stderr in logs when a run fails (Render/Docker).
		"--no-warnings",
		"--no-part",
		// Abort a genuinely stalled connection, but give slow datacenter links
		// enough room to keep a real (progressing) download alive.
		"--socket-timeout", "30",
		// Keep playlist expansion ON so multi-item carousels (Instagram /p/,
		// multi-image tweets, Facebook albums) download every item, not just one.
		"--yes-playlist",
	}

	// Use a per-platform Netscape cookie file if one was provided via env
	// (see ensureCookiesFileFromEnv); without a file we run anonymously.
	if ck := CookiesPathForURL(url); ck != "" {
		args = append(args, "--cookies", ck)
	} else {
		args = append(args, "--no-cookies")
	}

	// Always send a browser User-Agent so platforms that reject yt-dlp's default
	// UA still serve public media. This is cheap and covers the common case.
	ua := browserUA
	if opts.UserAgent != "" {
		ua = opts.UserAgent
	}
	args = append(args, "--user-agent", ua)

	// Browser IMPERSONATION (curl_cffi) is the stronger anti-block measure but
	// adds ~0.5s per request, so it's escalated to the fallback passes only — the
	// working platforms succeed on the fast pass and never pay for it. Empty when
	// curl_cffi isn't installed. --impersonate's Chrome identity matches browserUA.
	var impersonateArgs []string
	if t := strings.TrimSpace(e.Impersonate); t != "" {
		impersonateArgs = []string{"--impersonate", t}
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
	if isMedia {
		mediaArgs := append([]string{}, args...)
		mediaArgs = append(mediaArgs, impersonateArgs...) // image posts are rarer; give them the anti-block muscle
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
	if isFB {
		// Facebook playlists / reels: not every entry is mp4; prefer a progressive
		// mp4 first (no merge), then fall back to merged streams.
		fastFormat = fmt.Sprintf("best[ext=mp4][height<=%s]/bestvideo[height<=%s]+bestaudio/best[height<=%s]/best", maxH, maxH, maxH)
	}
	if opts.MaxFilesize != "" {
		// Best effort to stay under the Telegram bot limit (approx).
		fastFormat = fmt.Sprintf("best[ext=mp4][height<=%s][filesize<%s]/best[height<=%s][filesize<%s]/best[ext=mp4][filesize<%s]/best", maxH, opts.MaxFilesize, maxH, opts.MaxFilesize, opts.MaxFilesize)
		if isFB {
			fastFormat = fmt.Sprintf("best[ext=mp4][height<=%s][filesize<%s]/bestvideo[height<=%s]+bestaudio/best[ext=mp4][filesize<%s]/best[ext=webm][filesize<%s]/best[filesize<%s]/best", maxH, opts.MaxFilesize, maxH, opts.MaxFilesize, opts.MaxFilesize, opts.MaxFilesize)
		}
	}
	fastArgs = append(fastArgs, "-f", fastFormat)
	fastArgs = append(fastArgs,
		"--print", "after_move:filepath",
		"-o", out,
		"--", url,
	)

	files, err := runYtDlpAndCollectFiles(ctx, cmd, fastArgs, jobDir)
	if err == nil && len(files) > 0 {
		return &model.DownloadResult{Files: files, Size: totalSize(files)}, nil
	}
	// Reliability-first: never give up after the fast pass. A datacenter IP often
	// returns an ambiguous "not available / sign in / rate-limited" that a
	// different format selector or a retry actually clears, so always fall through
	// to the quality and compat passes below.

	// Quality fallback: best MP4 video + best M4A audio (may require merge).
	// Escalate to browser impersonation here — the fast pass already failed.
	qualityArgs := append([]string{}, args...)
	qualityArgs = append(qualityArgs, impersonateArgs...)
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
		compatArgs = append(compatArgs, impersonateArgs...)
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
cookiefile = sys.argv[3] if len(sys.argv) > 3 else ""

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

# Load an Instagram session from a Netscape cookies file if provided. A logged-in
# session is what lets Instagram work from a flagged datacenter IP.
if cookiefile and os.path.exists(cookiefile):
    try:
        import http.cookiejar
        cj = http.cookiejar.MozillaCookieJar(cookiefile)
        cj.load(ignore_discard=True, ignore_expires=True)
        L.context._session.cookies.update(cj)
    except Exception as e:
        print("COOKIE_LOAD_WARN:", repr(e))

try:
    post = Post.from_shortcode(L.context, shortcode)
    L.download_post(post, target=".")
    print("OK")
except Exception as e:
    print("RUNTIME_ERROR:", repr(e))
    raise
`

	cookieFile := CookiesPathForURL(url) // "" when no Instagram cookies were provided
	var lastErr error
	var lastOut string
	for _, candidate := range resolved {
		res, err := execx.Run(ctx, candidate, "-c", code, shortcode, jobDir, cookieFile)
		out := strings.TrimSpace(res.Output)
		if err == nil {
			lastErr = nil
			lastOut = out
			break
		}
		lastErr = err
		lastOut = out
		// If THIS interpreter has instaloader (the failure isn't a missing-module
		// error), it is authoritative: its error is the real reason the download
		// failed — e.g. Instagram's 403 / login-required on anonymous photo posts.
		// Stop here so a later interpreter that lacks the module can't overwrite it
		// with a misleading "No module named 'instaloader'".
		if !instaloaderModuleMissing(out) {
			break
		}
	}
	if lastErr != nil {
		// Module genuinely absent from every interpreter: mark the engine unavailable
		// so the pipeline falls back immediately instead of retrying.
		if instaloaderModuleMissing(lastOut) {
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

// instaloaderModuleMissing reports whether a Python run failed because the
// instaloader module isn't importable (vs. instaloader running and failing for a
// real reason, e.g. Instagram's 403). Used to avoid a later interpreter that lacks
// the module masking the authoritative interpreter's real error.
func instaloaderModuleMissing(out string) bool {
	if strings.Contains(out, "No module named 'instaloader'") ||
		strings.Contains(out, "No module named \"instaloader\"") {
		return true
	}
	return strings.Contains(out, "IMPORT_ERROR:") && strings.Contains(out, "ModuleNotFoundError")
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

// detectImpersonateTarget probes whether this yt-dlp can impersonate a browser
// (needs curl_cffi). It returns the best available client name ("chrome", …) to
// pass to --impersonate, or "" when impersonation is unavailable — so the engine
// self-configures and never passes --impersonate to a yt-dlp that can't do it
// (which would error). Run once at startup.
func detectImpersonateTarget(cmd string) string {
	if strings.TrimSpace(cmd) == "" {
		cmd = "yt-dlp"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, cmd, "--list-impersonate-targets").CombinedOutput()
	if err != nil {
		return ""
	}
	// A line names a client (Chrome, Edge, Safari, Firefox, …); if its Source
	// column says "(unavailable)" the required backend isn't installed. Prefer
	// Chrome, then other mainstream browsers.
	available := map[string]bool{}
	for _, ln := range strings.Split(string(out), "\n") {
		low := strings.ToLower(strings.TrimSpace(ln))
		if low == "" || strings.Contains(low, "unavailable") {
			continue
		}
		for _, target := range []string{"chrome", "edge", "safari", "firefox"} {
			if strings.HasPrefix(low, target) {
				available[target] = true
			}
		}
	}
	for _, target := range []string{"chrome", "edge", "safari", "firefox"} {
		if available[target] {
			return target
		}
	}
	return ""
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

