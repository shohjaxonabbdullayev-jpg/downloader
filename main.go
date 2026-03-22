
package main

import (
	"context"
	"encoding/base64"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"

	"telegram_bot_downloader/internal/cache"
	"telegram_bot_downloader/internal/downloader"
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
		Cache:    cache.FileCache{Root: ""},
		Semaphore: worker.NewSemaphore(maxConcurrentDownloads),
		DownloadsRoot: downloadsDir,
	}
	dl.CacheRootDefault()
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
	b64 := strings.TrimSpace(os.Getenv(envVar))
	if b64 == "" {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		log.Printf("cookies env decode failed (%s): %v", envVar, err)
		return
	}
	if err := os.WriteFile(targetPath, raw, 0600); err != nil {
		log.Printf("cookies write failed (%s -> %s): %v", envVar, targetPath, err)
		return
	}
	log.Printf("cookies file written: %s", targetPath)
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

	waitMsg, err := bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda..."))
	if err != nil {
		log.Printf("[send] chat_id=%d err=%v", chatID, err)
		return
	}

	defer bot.Request(tgbotapi.DeleteMessageConfig{
		ChatID:    chatID,
		MessageID: waitMsg.MessageID,
	})

	for _, link := range links {
		jobID, jobDir, err := downloader.NewJobDir(downloadsDir)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Yuklab bo‘lmadi"))
			continue
		}
		// Overall job timeout for yt-dlp / instaloader.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		ctx = downloader.ContextWithJobLogger(ctx, func(format string, args ...any) {
			log.Printf("["+jobID+"] "+format, args...)
		})

		// Skip expensive yt-dlp --dump-json unless truly needed.
		// Use simple heuristics for platform/type so the strategy can choose engines quickly.
		info := heuristicInfo(link)

		start := time.Now()
		res, err := dl.DownloadWithInfo(ctx, link, jobDir, info)
		log.Printf("[%s] download_time=%s", jobID, time.Since(start).Truncate(10*time.Millisecond))
		cancel()

		if err != nil || res == nil || len(res.Files) == 0 {
			if err != nil {
				log.Printf("[%s] download_failed url=%q err=%v", jobID, link, err)
			} else {
				log.Printf("[%s] download_failed url=%q (empty result)", jobID, link)
			}
			msgText := "❌ Yuklab bo‘lmadi"
			if err == downloader.ErrPrivate {
				msgText = "🔒 Bu kontent private (login kerak bo‘lishi mumkin)."
			} else if err == downloader.ErrNotFound {
				msgText = "❌ Kontent topilmadi yoki o‘chirib yuborilgan."
			}
			bot.Send(tgbotapi.NewMessage(chatID, msgText))
			_ = os.RemoveAll(jobDir)
			continue
		}

		mediaType := detectType(res.Files)
		for _, f := range res.Files {
			sendMedia(bot, chatID, f, msg.MessageID, mediaType)
			// Only delete temporary job files; cached files must remain.
			if strings.HasPrefix(f, jobDir) {
				_ = os.Remove(f)
			}
		}
		_ = os.RemoveAll(jobDir)
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
			typ = "image"
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
		strings.Contains(u, "twitter.com") ||
		strings.Contains(u, "x.com") ||
		strings.Contains(u, "facebook") ||
		strings.Contains(u, "fb.watch") ||
		strings.Contains(u, "pinterest") ||
		strings.Contains(u, "pin.it")
}

func detectType(files []string) string {
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" || ext == ".mov" {
			return "video"
		}
	}
	return "image"
}

/* ================= SENDER ================= */

func sendMedia(bot *tgbotapi.BotAPI, chatID int64, file string, replyTo int, mediaType string) {
	caption := "⬇️ @downloaderin123_bot"

	if mediaType == "video" {
		v := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(file))
		v.Caption = caption
		v.SupportsStreaming = true
		v.ReplyToMessageID = replyTo
		bot.Send(v)
	} else {
		p := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(file))
		p.Caption = caption
		p.ReplyToMessageID = replyTo
		bot.Send(p)
	}
}
