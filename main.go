package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

// ===================== CONFIG =====================

const (
	ffmpegPath    = "ffmpeg"
	ytDlpPath     = "yt-dlp"
	galleryDlPath = "gallery-dl"
)

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 3) // ✅ concurrency limit
)

// ===================== MAIN =====================

func main() {
	_ = godotenv.Load()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN missing")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	_ = os.MkdirAll(downloadsDir, 0755)

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("🤖 Bot running as @%s", bot.Self.UserName)

	// ✅ Health check
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "OK")
		})
		log.Fatal(http.ListenAndServe(":"+port, nil))
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			go handleMessage(bot, update.Message)
		}
	}
}

// ===================== MESSAGE HANDLER =====================

func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)

	if text == "/start" {
		bot.Send(tgbotapi.NewMessage(chatID,
			"👋 Salom!\n\n🎥 Instagram, TikTok, X (Twitter), Facebook yoki Pinterest link yuboring — men hamma media-ni ENG YUQORI sifatda yuklab beraman.",
		))
		return
	}

	links := extractLinks(text)
	if len(links) == 0 {
		return
	}

	waitMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda..."))

	for _, link := range links {
		go func(l string) {
			sem <- struct{}{}
			files, mediaType, err := download(l)
			<-sem

			// silence loading message
			bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: waitMsg.MessageID,
			})

			if err != nil || len(files) == 0 {
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Linkni tekshiring."))
				return
			}

			for _, f := range files {
				sendMedia(bot, chatID, f, msg.MessageID, mediaType)
				_ = os.Remove(f)
			}
		}(link)
	}
}

// ===================== LINK PARSING =====================

func extractLinks(text string) []string {
	re := regexp.MustCompile(`https?://\S+`)
	raw := re.FindAllString(text, -1)

	var out []string
	for _, u := range raw {
		if isSupported(u) {
			out = append(out, u)
		}
	}
	return out
}

func isSupported(u string) bool {
	u = strings.ToLower(u)
	return strings.Contains(u, "instagram") ||
		strings.Contains(u, "instagr.am") ||
		strings.Contains(u, "tiktok") ||
		strings.Contains(u, "twitter.com") ||
		strings.Contains(u, "x.com") ||
		strings.Contains(u, "facebook") ||
		strings.Contains(u, "fb.watch") ||
		strings.Contains(u, "pinterest") ||
		strings.Contains(u, "pin.it")
}

// ===================== DOWNLOAD CORE =====================

func download(link string) ([]string, string, error) {
	start := time.Now()

	out := filepath.Join(downloadsDir,
		fmt.Sprintf("%d_%%(title)s_%%(id)s.%%(ext)s", time.Now().Unix()),
	)

	args := []string{
		"--no-warnings",
		"--yes-playlist",                        // ✅ multi-media
		"--merge-output-format", "mp4",         // ✅ clean mp4
		"-f", "bv*[height<=2160]+ba/best",       // ✅ BEST quality up to 4K
		"--postprocessor-args", "ffmpeg:-movflags +faststart",
		"-o", out,
		link,
	}

	applyCookies(&args, link)

	_, _ = run(ytDlpPath, args...)

	files := recentFiles(start)
	if len(files) > 0 {
		mType := detectType(files)
		return files, mType, nil
	}

	// ✅ Fallback gallery-dl (for images / stories)
	run(galleryDlPath,
		"--write-metadata",
		"--write-info-json",
		"-d", downloadsDir,
		link,
	)

	files = recentFiles(start)
	if len(files) > 0 {
		return files, "image", nil
	}

	return nil, "", fmt.Errorf("download failed")
}

// ===================== EXEC TOOL =====================

func run(cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...)
	var b bytes.Buffer
	c.Stdout = &b
	c.Stderr = &b
	err := c.Run()
	return b.String(), err
}

// ===================== FILE FINDER =====================

func recentFiles(since time.Time) []string {
	var files []string

	filepath.Walk(downloadsDir, func(path string, info os.FileInfo, err error) error {
		if info != nil && !info.IsDir() && info.ModTime().After(since) {
			files = append(files, path)
		}
		return nil
	})

	sort.Strings(files)
	return files
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

// ===================== MEDIA SENDER =====================

func sendMedia(bot *tgbotapi.BotAPI, chatID int64, file string, replyTo int, mediaType string) {
	caption := "⬇️ @downloaderin123_bot orqali yuklab olindi"

	if mediaType == "video" {
		v := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(file))
		v.Caption = caption
		v.ReplyToMessageID = replyTo
		bot.Send(v)
	} else {
		p := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(file))
		p.Caption = caption
		p.ReplyToMessageID = replyTo
		bot.Send(p)
	}
}

// ===================== COOKIES =====================

func applyCookies(args *[]string, link string) {
	if strings.Contains(link, "instagram") && fileExists("instagram.txt") {
		*args = append([]string{"--cookies", "instagram.txt"}, *args...)
	}
	if strings.Contains(link, "twitter") && fileExists("twitter.txt") {
		*args = append([]string{"--cookies", "twitter.txt"}, *args...)
	}
	if strings.Contains(link, "facebook") && fileExists("facebook.txt") {
		*args = append([]string{"--cookies", "facebook.txt"}, *args...)
	}
	if strings.Contains(link, "pinterest") && fileExists("pinterest.txt") {
		*args = append([]string{"--cookies", "pinterest.txt"}, *args...)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
