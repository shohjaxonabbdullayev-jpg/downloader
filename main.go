i.NewInlineKeyboardButtonURL(
				"👥 Guruhga qo‘shish",
				fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName),
			),
		),
	)

	bot.Send(
		tgbotapi.Newpackage main

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

const (
	ffmpegPath     = "ffmpeg"
	ytDlpPath      = "yt-dlp"
	galleryDlPath  = "gallery-dl"
	maxVideoHeight = 1080
)

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 3) // concurrency limit
)

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

	os.MkdirAll(downloadsDir, 0755)

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("🤖 Bot running as @%s", bot.Self.UserName)

	// Health check server
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprint(w, "OK")
		})
		log.Printf("💚 Health check server running on port %s", port)
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

// ===================== HANDLE MESSAGES =====================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	chatID := msg.Chat.ID

	if text == "/start" {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf(
			"👋 Salom %s!\n\n🎥 YouTube, Instagram, Pinterest, TikTok, Facebook yoki Twitter link yuboring — men videoni yoki rasmni yuboraman.",
			msg.From.FirstName)))
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

			// Delete loading message
			bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: waitMsg.MessageID})

			if err != nil || len(files) == 0 {
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Linkni tekshiring."))
				return
			}

			for _, f := range files {
				sendMedia(bot, chatID, f, msg.MessageID, mediaType)
				os.Remove(f)
			}
		}(link)
	}
}

// ===================== LINK PARSING =====================
func extractLinks(text string) []string {
	re := regexp.MustCompile(https?://\S+)
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
	return strings.Contains(u, "youtube") ||
		strings.Contains(u, "youtu.be") ||
		strings.Contains(u, "instagram") ||
		strings.Contains(u, "instagr.am") ||
		strings.Contains(u, "pinterest") ||
		strings.Contains(u, "pin.it") ||
		strings.Contains(u, "tiktok") ||
		strings.Contains(u, "facebook") ||
		strings.Contains(u, "fb.watch") ||
		strings.Contains(u, "twitter.com") ||
		strings.Contains(u, "x.com")
}

// ===================== DOWNLOAD =====================
func download(link string) ([]string, string, error) {
	start := time.Now()
	out := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", time.Now().Unix()))
	args := []string{"--no-warnings", "-f", fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best/best", maxVideoHeight), "--merge-output-format", "mp4", "-o", out, link}

	// Optional cookie files
	if strings.Contains(link, "youtube") || strings.Contains(link, "youtu.be") {
		if fileExists("youtube.txt") {
			args = append([]string{"--cookies", "youtube.txt"}, args...)
		}
	}
	if strings.Contains(link, "instagram") || strings.Contains(link, "instagr.am") {
		if fileExists("instagram.txt") {
			args = append([]string{"--cookies", "instagram.txt"}, args...)
		}
	}
	if strings.Contains(link, "pinterest") || strings.Contains(link, "pin.it") {
		if fileExists("pinterest.txt") {
			args = append([]string{"--cookies", "pinterest.txt"}, args...)
		}
	}
	if strings.Contains(link, "twitter.com") || strings.Contains(link, "x.com") {
		if fileExists("twitter.txt") {
			args = append([]string{"--cookies", "twitter.txt"}, args...)
		}
	}
	if strings.Contains(link, "facebook") || strings.Contains(link, "fb.watch") {
		if fileExists("facebook.txt") {
			args = append([]string{"--cookies", "facebook.txt"}, args...)
		}
	}

	// Try yt-dlp first
	_, _ = run(ytDlpPath, args...)
	files := recentFiles(start)
	if len(files) > 0 {
		mediaType := "image"
		for _, f := range files {
			ext := strings.ToLower(filepath.Ext(f))
			if ext == ".mp4" || ext == ".mov" {
				mediaType = "video"
				break
			}
		}
		return files, mediaType, nil
	}

	// Fallback: gallery-dl for images (Twitter/X & Facebook)
	if strings.Contains(link, "twitter.com") || strings.Contains(link, "x.com") || strings.Contains(link, "facebook") || strings.Contains(link, "fb.watch") {
		run(galleryDlPath, "-d", downloadsDir, link)
		files = recentFiles(start)
		if len(files) > 0 {
			return files, "image", nil
		}
	}

	// Pinterest/Instagram gallery fallback
	if strings.Contains(link, "pinterest") || strings.Contains(link, "pin.it") || strings.Contains(link, "instagram") {
		run(galleryDlPath, "-d", downloadsDir, link)
		files = recentFiles(start)
		if len(files) > 0 {
			return files, "image", nil
		}
	}

	return nil, "", fmt.Errorf("download failed")
}

func run(cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...)
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	err := c.Run()
	return buf.String(), err
}

func recentFiles(since time.Time) []string {
	var files []string
	filepath.Walk(downloadsDir, func(p string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() && info.ModTime().After(since) {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// ===================== SEND MEDIA WITH INLINE SHARE =====================
func sendMedia(bot *tgbotapi.BotAPI, chatID int64, file string, replyTo int, mediaType string) {
	caption := "@downloaderin123_bot orqali yuklab olindi"

	var msg tgbotapi.Message
	var err error

	if mediaType == "video" {
		v := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(file))
		v.Caption = caption
		v.ReplyToMessageID = replyTo
		msg, err = bot.Send(v)
	} else {
		p := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(file))
		p.Caption = caption
		p.ReplyToMessageID = replyTo
		msg, err = bot.Send(p)
	}

	if err != nil {
		log.Println("Send error:", err)
		return
	}

	// Inline buttons: first row = forward via inline, second row = add bot to group
	btnShare := tgbotapi.NewInlineKeyboardButtonSwitch("📤 Ulashish", "") // inline mode
	btnGroup := tgbotapi.NewInlineKeyboardButtonURL("👥 Guruhga qo‘shish", fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(btnShare),
		tgbotapi.NewInlineKeyboardRow(btnGroup),
	)

	bot.Send(tgbotapi.NewEditMessageReplyMarkup(chatID, msg.MessageID, keyboard))
}

// ===================== HELPERS =====================
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

