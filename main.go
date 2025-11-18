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
	ytDlpPath     = "yt-dlp"
	galleryDlPath = "gallery-dl"
)

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 3) // max 3 simultaneous downloads
)

func main() {
	godotenv.Load()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("BOT_TOKEN missing")
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

	log.Printf("Bot: @%s", bot.Self.UserName)

	// health endpoint
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, "OK")
		})
		http.ListenAndServe(":"+port, nil)
	}()

	updateCfg := tgbotapi.NewUpdate(0)
	updateCfg.Timeout = 20
	updates := bot.GetUpdatesChan(updateCfg)

	for update := range updates {
		if update.Message != nil {
			go handleMessage(bot, update.Message)
		}
	}
}

func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	chatID := msg.Chat.ID

	if text == "/start" {
		bot.Send(tgbotapi.NewMessage(chatID, "👋 Salom! Link yuboring — yuklab beraman."))
		return
	}

	links := extractLinks(text)
	if len(links) == 0 {
		return
	}

	waiting, _ := bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda..."))

	for _, link := range links {
		go func(url string) {
			sem <- struct{}{}
			files, fileType, err := download(url)
			<-sem

			bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: waiting.MessageID,
			})

			if err != nil || len(files) == 0 {
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi."))
				return
			}

			for _, f := range files {
				sendFile(bot, chatID, f, msg.MessageID, fileType)
				os.Remove(f)
			}

		}(link)
	}
}

// =========================================
//  LINK PARSING
// =========================================

func extractLinks(text string) []string {
	re := regexp.MustCompile(`https?://\S+`)
	all := re.FindAllString(text, -1)
	var filtered []string

	for _, u := range all {
		if isSupported(u) {
			filtered = append(filtered, u)
		}
	}
	return filtered
}

func isSupported(u string) bool {
	u = strings.ToLower(u)
	return strings.Contains(u, "youtube") ||
		strings.Contains(u, "youtu.be") ||
		strings.Contains(u, "instagram") ||
		strings.Contains(u, "instagr.am") ||
		strings.Contains(u, "tiktok") ||
		strings.Contains(u, "pinterest") ||
		strings.Contains(u, "pin.it") ||
		strings.Contains(u, "facebook") ||
		strings.Contains(u, "fb.watch") ||
		strings.Contains(u, "twitter") ||
		strings.Contains(u, "x.com")
}

// =========================================
//  DOWNLOAD LOGIC
// =========================================

func download(link string) ([]string, string, error) {
	start := time.Now()

	// output file template
	out := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", start.Unix()))

	var format string
	if strings.Contains(link, "youtu") {
		format = "bestvideo[height<=720]+bestaudio/best"
	} else {
		format = "bv*+ba/best"
	}

	args := []string{
		"--no-warnings",
		"--no-call-home",
		"-f", format,
		"--merge-output-format", "mp4",
		"-o", out,
		link,
	}

	run(ytDlpPath, args...)

	// detect downloaded files
	files := recentFiles(start)
	if len(files) > 0 {
		if isVideo(files) {
			return files, "video", nil
		}
		return files, "image", nil
	}

	// fallback: gallery-dl
	run(galleryDlPath, "-d", downloadsDir, link)

	files = recentFiles(start)
	if len(files) > 0 {
		if isVideo(files) {
			return files, "video", nil
		}
		return files, "image", nil
	}

	return nil, "", fmt.Errorf("failed to download")
}

func isVideo(files []string) bool {
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" || ext == ".mov" || ext == ".mkv" {
			return true
		}
	}
	return false
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
	var out []string

	filepath.Walk(downloadsDir, func(path string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() && info.ModTime().After(since) {
			out = append(out, path)
		}
		return nil
	})

	sort.Strings(out)
	return out
}

// =========================================
//  SEND FILE
// =========================================

func sendFile(bot *tgbotapi.BotAPI, chatID int64, path string, replyTo int, fileType string) {
	caption := "@downloaderin123_bot orqali yuklab olindi"

	var msg tgbotapi.Message
	var err error

	if fileType == "video" {
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(path))
		video.Caption = caption
		video.ReplyToMessageID = replyTo
		msg, err = bot.Send(video)
	} else {
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(path))
		photo.Caption = caption
		photo.ReplyToMessageID = replyTo
		msg, err = bot.Send(photo)
	}

	if err != nil {
		log.Println("send error:", err)
		return
	}

	// share buttons
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonSwitch("📤 Ulashish", ""),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL(
				"👥 Guruhga qo‘shish",
				fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName),
			),
		),
	)

	bot.Send(
		tgbotapi.NewEditMessageReplyMarkup(chatID, msg.MessageID, kb),
	)
}

