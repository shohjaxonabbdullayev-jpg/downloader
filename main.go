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

const (
	ffmpegPath     = "ffmpeg"
	ytDlpPath      = "yt-dlp"
	galleryDlPath  = "gallery-dl"
	maxVideoHeight = 720
)

var (
	downloadsDir = "downloads"
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
	log.Printf("🤖 Bot started as @%s", bot.Self.UserName)

	// Health check
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("OK"))
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

// ===================== HANDLE MESSAGE =====================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)

	if text == "/start" {
		bot.Send(tgbotapi.NewMessage(chatID,
			fmt.Sprintf("👋 Salom %s!\n\n🎥 Link yuboring: Instagram, TikTok, Pinterest, Facebook yoki Twitter – men videoni yoki rasmni yuklab beraman.",
				msg.From.FirstName)))
		return
	}

	links := extractLinks(text)
	if len(links) == 0 {
		return
	}

	loadingMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda..."))

	// Sequential processing: download one link at a time in order
	for _, link := range links {
		files, mediaType, err := download(link)
		if err != nil || len(files) == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ Yuklab bo‘lmadi: %s", link)))
			continue
		}

		for _, f := range files {
			sendMediaWithButtons(bot, chatID, f, mediaType)
			os.Remove(f)
		}
	}

	// Delete the "loading" message
	_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
		ChatID:    chatID,
		MessageID: loadingMsg.MessageID,
	})
}

// ===================== LINK PARSING =====================
func extractLinks(text string) []string {
	re := regexp.MustCompile(`https?://\S+`)
	found := re.FindAllString(text, -1)
	var out []string
	for _, u := range found {
		if isSupported(u) {
			out = append(out, u)
		}
	}
	return out
}

func isSupported(u string) bool {
	u = strings.ToLower(u)
	return strings.Contains(u, "instagram") ||
		strings.Contains(u, "tiktok") ||
		strings.Contains(u, "pinterest") ||
		strings.Contains(u, "facebook") ||
		strings.Contains(u, "fb.watch") ||
		strings.Contains(u, "twitter") ||
		strings.Contains(u, "x.com")
}

// ===================== DOWNLOAD =====================
func download(link string) ([]string, string, error) {
	start := time.Now()
	out := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", time.Now().Unix()))

	args := []string{
		"--no-warnings",
		"-f", "bestvideo+bestaudio/best",
		"--merge-output-format", "mp4",
		"-o", out,
		link,
	}

	run(ytDlpPath, args...)
	files := recentFiles(start)
	if len(files) > 0 {
		return files, detectMediaType(files), nil
	}

	run(galleryDlPath, "-d", downloadsDir, link)
	files = recentFiles(start)
	if len(files) > 0 {
		return files, "image", nil
	}

	return nil, "", fmt.Errorf("download failed")
}

func detectMediaType(files []string) string {
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" || ext == ".mov" || ext == ".mkv" {
			return "video"
		}
	}
	return "image"
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

// ===================== SEND MEDIA WITH BUTTONS =====================
func sendMediaWithButtons(bot *tgbotapi.BotAPI, chatID int64, file, mediaType string) {
	caption := "@downloaderin123_bot orqali yuklab olindi"

	var msg tgbotapi.Message
	var err error

	if mediaType == "video" {
		v := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(file))
		v.Caption = caption
		msg, err = bot.Send(v)
	} else {
		p := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(file))
		p.Caption = caption
		msg, err = bot.Send(p)
	}

	if err != nil {
		log.Println("send error:", err)
		return
	}

	// Buttons for sharing bot link & adding to group
	btnShare := tgbotapi.NewInlineKeyboardButtonURL(
		"📤 Do‘stlar bilan ulashish",
		fmt.Sprintf("https://t.me/%s", bot.Self.UserName),
	)
	btnGroup := tgbotapi.NewInlineKeyboardButtonURL(
		"👥 Guruhga qo‘shish",
		fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName),
	)

	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(btnShare),
		tgbotapi.NewInlineKeyboardRow(btnGroup),
	)

	_, _ = bot.Request(tgbotapi.NewEditMessageReplyMarkup(chatID, msg.MessageID, kb))
}
