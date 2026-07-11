
package main

import (
	"context"
	"encoding/base64"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"

	"telegram_bot_downloader/internal/cache"
	"telegram_bot_downloader/internal/downloader"
	"telegram_bot_downloader/internal/fidcache"
	"telegram_bot_downloader/internal/platforms"
	"telegram_bot_downloader/internal/urlx"
	"telegram_bot_downloader/internal/worker"
)

/* ================= CONFIG ================= */

const (
	downloadsDir = "downloads"
)

// Tune based on your CPU + bandwidth. 8 is a good default on most servers.
const maxConcurrentDownloads = 8

var linkURLRe = regexp.MustCompile(`https?://\S+`)

// fidCache maps a link to the Telegram file_id(s) of media already uploaded for
// it. Repeat requests re-send by file_id (instant, no download/upload) and keep
// nothing on disk.
var fidCache = fidcache.New(5000)

/* ================= MAIN ================= */

func main() {
	_ = godotenv.Load()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("BOT_TOKEN missing")
	}

	// Optional Netscape cookies (see yt-dlp --cookies). Base64 writes the file at startup.
	ensureCookiesFileFromEnv("INSTAGRAM_COOKIES_B64", "instagram.txt")
	ensureCookiesFileFromEnv("TWITTER_COOKIES_B64", "twitter.txt")
	ensureCookiesFileFromEnv("FACEBOOK_COOKIES_B64", "facebook.txt")
	ensureCookiesFileFromEnv("PINTEREST_COOKIES_B64", "pinterest.txt")

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dl := &downloader.PipelineDownloader{
		Detector: downloader.YtDlpDetector{Cmd: "yt-dlp"},
		Registry: platforms.DefaultRegistry(),
		// Cache disabled (Root left empty): media is deleted right after it is
		// sent, so nothing is kept on disk. (Enable via CacheRootDefault() if
		// instant re-sends of identical links ever become worth the disk.)
		Cache:         cache.FileCache{Root: ""},
		Semaphore:     worker.NewSemaphore(maxConcurrentDownloads),
		DownloadsRoot: downloadsDir,
	}
	if err := dl.EnsureDirs(); err != nil {
		log.Fatal(err)
	}

	// Optional TTL cleanup for old job folders (best-effort).
	_ = worker.StartTTLReaper(downloadsDir, "job_", 2*time.Hour, 30*time.Minute)

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}
	bot.Debug = false

	// Ensure long-polling works even if a webhook was previously set.
	_, _ = bot.Request(tgbotapi.DeleteWebhookConfig{DropPendingUpdates: true})

	log.Printf("Bot started: @%s", bot.Self.UserName)

	// Health check (Render / Railway)
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		})
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			log.Printf("health server stopped: %v", err)
		}
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	for update := range bot.GetUpdatesChan(u) {
		if update.Message != nil {
			log.Printf("[update] chat_id=%d from=%s text=%q", update.Message.Chat.ID, update.Message.From.UserName, update.Message.Text)
		} else {
			log.Printf("[update] non-message update received")
		}
		if update.Message != nil {
			go handleMessage(bot, dl, update.Message)
		}
	}
}

func ensureCookiesFileFromEnv(envVar string, targetPath string) {
	// Strip ALL whitespace/newlines so wrapped base64 (e.g. `base64` without -w0)
	// still decodes.
	b64 := strings.Join(strings.Fields(os.Getenv(envVar)), "")
	if b64 == "" {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		log.Printf("cookies env decode failed (%s): %v", envVar, err)
		return
	}
	// Normalize CRLF -> LF; yt-dlp rejects Windows line endings in cookie files on Linux.
	raw = []byte(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	if err := os.WriteFile(targetPath, raw, 0600); err != nil {
		log.Printf("cookies write failed (%s -> %s): %v", envVar, targetPath, err)
		return
	}
	log.Printf("cookies file written: %s (%d bytes)", targetPath, len(raw))
}

/* ================= MESSAGE HANDLER ================= */

func handleMessage(bot *tgbotapi.BotAPI, dl *downloader.PipelineDownloader, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)

	if text == "/start" {
		if _, err := bot.Send(tgbotapi.NewMessage(
			chatID,
			"👋 Salom!\n\nInstagram, TikTok, X, Facebook yoki Pinterest link yuboring.\nVideo va rasmlarni **eng mos va ochiladigan formatda** yuklab beraman 🚀",
		)); err != nil {
			log.Printf("[send] chat_id=%d err=%v", chatID, err)
		}
		return
	}

	links := extractLinks(text)
	if len(links) == 0 {
		return
	}

	for _, link := range links {
		// YouTube isn't supported and we stay silent for it — no reply at all.
		// Skip the link so a YouTube-only message produces no response, while any
		// other supported links in the same message are still handled.
		if urlx.PlatformFromURL(link) == "youtube" {
			continue
		}

		key := cacheKeyForURL(link)

		// Fast path: this link was uploaded before -> re-send by Telegram file_id.
		// No download, no re-upload, no loading message => ~1s, and nothing on disk.
		if items, ok := fidCache.Get(key); ok {
			if sendCachedAll(bot, chatID, items, msg.MessageID) {
				log.Printf("[cache] file_id hit url=%q files=%d", link, len(items))
				continue
			}
			fidCache.Delete(key) // stale file_id(s) -> fall through to a fresh fetch
		}

		// Cold path: show a loading indicator while we fetch.
		waitMsg, werr := bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda..."))

		// Heuristic platform/type avoids an expensive yt-dlp --dump-json probe.
		info := heuristicInfo(link)

		jobID, jobDir, jerr := downloader.NewJobDir(downloadsDir)
		if jerr != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Yuklab bo‘lmadi"))
			deleteMsg(bot, chatID, waitMsg, werr)
			continue
		}
		// Overall job timeout for yt-dlp / instaloader.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		ctx = downloader.ContextWithJobLogger(ctx, func(format string, args ...any) {
			log.Printf("["+jobID+"] "+format, args...)
		})

		start := time.Now()
		res, derr := dl.DownloadWithInfo(ctx, link, jobDir, info)
		log.Printf("[%s] download_time=%s", jobID, time.Since(start).Truncate(10*time.Millisecond))
		cancel()

		if derr != nil || res == nil || len(res.Files) == 0 {
			if derr != nil {
				log.Printf("[%s] download_failed url=%q err=%v", jobID, link, derr)
			} else {
				log.Printf("[%s] download_failed url=%q (empty result)", jobID, link)
			}
			msgText := "❌ Yuklab bo‘lmadi"
			if derr == downloader.ErrPrivate {
				msgText = "🔒 Bu kontent private (login kerak bo‘lishi mumkin)."
			} else if derr == downloader.ErrNotFound {
				msgText = "❌ Kontent topilmadi yoki o‘chirib yuborilgan."
			}
			bot.Send(tgbotapi.NewMessage(chatID, msgText))
			_ = os.RemoveAll(jobDir)
			deleteMsg(bot, chatID, waitMsg, werr)
			continue
		}

		sendStart := time.Now()
		var captured []fidcache.Item
		for _, f := range res.Files {
			if kind, fid := sendMedia(bot, chatID, f, msg.MessageID); fid != "" {
				captured = append(captured, fidcache.Item{Kind: kind, FileID: fid})
			}
		}
		log.Printf("[%s] send_time=%s files=%d", jobID, time.Since(sendStart).Truncate(10*time.Millisecond), len(res.Files))

		// Cache the file_ids so the next request for this link is instant.
		fidCache.Put(key, captured)

		// Free disk immediately: media is sent, nothing is kept on disk.
		_ = os.RemoveAll(jobDir)
		deleteMsg(bot, chatID, waitMsg, werr)
	}
}

func heuristicInfo(rawURL string) *downloader.MediaInfo {
	u := strings.ToLower(rawURL)
	plat := urlx.PlatformFromURL(u)
	typ := "unknown"
	// Instagram has clear URL shapes.
	if plat == "instagram" {
		switch {
		case strings.Contains(u, "/reel/") || strings.Contains(u, "/tv/"):
			typ = "video"
		case strings.Contains(u, "/p/"):
			// A /p/ post can be a single photo, a multi-item carousel, or a video;
			// "carousel" routes it through the permissive all-items image path.
			typ = "carousel"
		}
	}
	if plat == "facebook" {
		// Tag the type so yt-dlp picks a video vs. permissive (photo) format pass;
		// Facebook runs on yt-dlp only (no gallery-dl).
		videoish := strings.Contains(u, "fb.watch") ||
			strings.Contains(u, "/reel") ||
			strings.Contains(u, "/videos/") ||
			strings.Contains(u, "video.php") ||
			(strings.Contains(u, "facebook.com/watch") && strings.Contains(u, "v="))
		if videoish {
			typ = "video"
		} else {
			typ = "carousel"
		}
	}
	return &downloader.MediaInfo{Platform: plat, Type: typ}
}

/* ================= LINK PARSER ================= */

func extractLinks(text string) []string {
	raw := linkURLRe.FindAllString(text, -1)

	var links []string
	for _, u := range raw {
		if isSupported(u) {
			links = append(links, u)
		}
	}
	return links
}

func isSupported(u string) bool {
	u = strings.ToLower(u)
	return strings.Contains(u, "instagram") ||
		strings.Contains(u, "tiktok") ||
		strings.Contains(u, "youtube.com") ||
		strings.Contains(u, "youtu.be") ||
		strings.Contains(u, "twitter.com") ||
		strings.Contains(u, "x.com") ||
		strings.Contains(u, "facebook") ||
		strings.Contains(u, "fb.watch") ||
		strings.Contains(u, "pinterest") ||
		strings.Contains(u, "pin.it")
}

// isVideoFile reports whether a downloaded file is a video (sent as a Telegram
// video with an inline player) vs an image (sent as a photo). Detected per file
// so a mixed carousel sends each item with the correct type.
func isVideoFile(file string) bool {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".mp4", ".mov", ".webm", ".mkv", ".avi", ".m4v":
		return true
	}
	return false
}

// videoDurationSeconds returns the video's duration via ffprobe (0 if unknown).
// Passing the duration helps Telegram render a proper, correctly-sized player
// across clients instead of a generic preview.
func videoDurationSeconds(file string) int {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", file).Output()
	if err != nil {
		return 0
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || secs <= 0 {
		return 0
	}
	return int(secs + 0.5)
}

// trackingParams are share/analytics query params that don't change the media,
// so they're stripped from the cache key to maximize file_id cache hits across
// differently-shared copies of the same link.
var trackingParams = map[string]bool{
	"igsh": true, "igshid": true, "si": true, "feature": true,
	"utm_source": true, "utm_medium": true, "utm_campaign": true,
	"utm_term": true, "utm_content": true, "fbclid": true, "gclid": true,
}

// cacheKeyForURL builds the file_id cache key: the URL minus fragment and
// tracking params, lower-cased host. Essential params (e.g. YouTube's v=) stay.
func cacheKeyForURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return downloader.HashURL(downloader.NormalizeURL(raw))
	}
	q := u.Query()
	for p := range trackingParams {
		q.Del(p)
	}
	u.RawQuery = q.Encode()
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	return downloader.HashURL(u.String())
}

/* ================= SENDER ================= */

// sendMedia uploads a downloaded file and returns the Telegram kind + file_id of
// the resulting message (empty on failure) so the link can be cached for instant
// re-sends.
func sendMedia(bot *tgbotapi.BotAPI, chatID int64, file string, replyTo int) (kind, fileID string) {
	caption := "⬇️ @downloaderin123_bot"

	if isVideoFile(file) {
		v := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(file))
		v.Caption = caption
		v.SupportsStreaming = true
		v.ReplyToMessageID = replyTo
		if d := videoDurationSeconds(file); d > 0 {
			v.Duration = d
		}
		m, err := bot.Send(v)
		if err != nil {
			log.Printf("[send] video chat_id=%d err=%v", chatID, err)
			return "", ""
		}
		return classifyMedia(m)
	}

	p := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(file))
	p.Caption = caption
	p.ReplyToMessageID = replyTo
	m, err := bot.Send(p)
	if err != nil {
		log.Printf("[send] photo chat_id=%d err=%v", chatID, err)
		return "", ""
	}
	return classifyMedia(m)
}

// classifyMedia extracts the kind + file_id Telegram assigned to a sent message.
func classifyMedia(m tgbotapi.Message) (kind, fileID string) {
	switch {
	case m.Video != nil:
		return "video", m.Video.FileID
	case m.Animation != nil:
		return "animation", m.Animation.FileID
	case m.Document != nil:
		return "document", m.Document.FileID
	case len(m.Photo) > 0:
		return "photo", m.Photo[len(m.Photo)-1].FileID // largest size
	}
	return "", ""
}

// sendCachedAll re-sends previously-uploaded media by file_id. Returns false only
// when nothing was sent (stale first file_id), so the caller can re-download
// without producing duplicates.
func sendCachedAll(bot *tgbotapi.BotAPI, chatID int64, items []fidcache.Item, replyTo int) bool {
	for i, it := range items {
		if err := sendByFileID(bot, chatID, it, replyTo); err != nil {
			log.Printf("[cache] file_id send failed (item %d): %v", i, err)
			if i == 0 {
				return false // nothing sent yet -> safe to re-download
			}
			return true // partial send already happened; don't duplicate
		}
	}
	return true
}

// sendByFileID re-sends one cached media item by its Telegram file_id.
func sendByFileID(bot *tgbotapi.BotAPI, chatID int64, it fidcache.Item, replyTo int) error {
	caption := "⬇️ @downloaderin123_bot"
	ref := tgbotapi.FileID(it.FileID)

	var c tgbotapi.Chattable
	switch it.Kind {
	case "video":
		v := tgbotapi.NewVideo(chatID, ref)
		v.Caption = caption
		v.SupportsStreaming = true
		v.ReplyToMessageID = replyTo
		c = v
	case "animation":
		a := tgbotapi.NewAnimation(chatID, ref)
		a.Caption = caption
		a.ReplyToMessageID = replyTo
		c = a
	case "document":
		d := tgbotapi.NewDocument(chatID, ref)
		d.Caption = caption
		d.ReplyToMessageID = replyTo
		c = d
	default: // photo
		p := tgbotapi.NewPhoto(chatID, ref)
		p.Caption = caption
		p.ReplyToMessageID = replyTo
		c = p
	}
	_, err := bot.Send(c)
	return err
}

// deleteMsg removes the transient "loading" message if it was sent.
func deleteMsg(bot *tgbotapi.BotAPI, chatID int64, m tgbotapi.Message, sendErr error) {
	if sendErr != nil {
		return
	}
	_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: m.MessageID})
}
