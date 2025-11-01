package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const downloadsDir = "downloads"

func main() {
	_ = godotenv.Load()
	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		log.Fatal("❌ BOT_TOKEN missing in .env")
	}

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("🤖 Authorized as @%s", bot.Self.UserName)
	os.MkdirAll(downloadsDir, os.ModePerm)

	// Health check server (needed for Render)
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		})
		log.Fatal(http.ListenAndServe(":10000", nil))
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil || update.Message.Text == "" {
			continue
		}

		go func(text string, chatID int64) {
			links := extractLinks(text)
			if len(links) == 0 {
				return
			}

			for _, link := range links {
				files, err := downloadMedia(link)
				if err != nil {
					log.Printf("❌ %v", err)
					bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ Yuklab bo‘lmadi: %v", err)))
					continue
				}
				for _, f := range files {
					sendMedia(bot, chatID, f)
					os.Remove(f)
				}
			}
		}(update.Message.Text, update.Message.Chat.ID)
	}
}

func extractLinks(text string) []string {
	parts := strings.Fields(text)
	var links []string
	for _, p := range parts {
		if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
			links = append(links, p)
		}
	}
	return links
}

func downloadMedia(url string) ([]string, error) {
	start := time.Now()
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	isYouTube := strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
	isInstagram := strings.Contains(url, "instagram.com")
	isPinterest := strings.Contains(url, "pinterest.com") || strings.Contains(url, "pin.it")
	isTikTok := strings.Contains(url, "tiktok.com")

	var cmd *exec.Cmd

	switch {
	case isYouTube:
		args := []string{"--no-warnings", "--no-playlist", "-o", outputTemplate, "-f", "best[height<=720]"}
		if fileExists("youtube_cookies.txt") {
			args = append(args, "--cookies", "youtube_cookies.txt")
		}
		args = append(args, url)
		cmd = exec.Command("yt-dlp", args...)

	case isInstagram:
		args := []string{"--no-warnings", "-o", outputTemplate}
		if fileExists("instagram_cookies.txt") {
			args = append(args, "--cookies", "instagram_cookies.txt")
		}
		args = append(args, url)
		cmd = exec.Command("yt-dlp", args...)

	case isPinterest:
		if fileExists("pinterest_cookies.txt") {
			cmd = exec.Command("gallery-dl", "--cookies", "pinterest_cookies.txt", "-d", downloadsDir, url)
		} else {
			cmd = exec.Command("gallery-dl", "-d", downloadsDir, url)
		}

	case isTikTok:
		args := []string{"--no-warnings", "-o", outputTemplate}
		cmd = exec.Command("yt-dlp", args...)

	default:
		return nil, fmt.Errorf("unsupported link type")
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("download failed: %v\n%s", err, out.String())
	}

	files := filesCreatedAfter(downloadsDir, start)
	if len(files) == 0 {
		return nil, fmt.Errorf("no media downloaded")
	}
	return files, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func filesCreatedAfter(dir string, t time.Time) []string {
	var res []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(t) {
			res = append(res, path)
		}
		return nil
	})
	return res
}

func sendMedia(bot *tgbotapi.BotAPI, chatID int64, filePath string) error {
	caption := "@downloaderin123_bot orqali yuklab olindi"
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL("🔗 Do‘stlarga ulashish", "https://t.me/downloaderin123_bot"),
		),
	)

	ext := strings.ToLower(filepath.Ext(filePath))
	if strings.HasSuffix(ext, ".jpg") || strings.HasSuffix(ext, ".png") || strings.HasSuffix(ext, ".webp") {
		msg := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		msg.Caption = caption
		msg.ReplyMarkup = keyboard
		_, err := bot.Send(msg)
		return err
	}

	msg := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = caption
	msg.ReplyMarkup = keyboard
	_, err := bot.Send(msg)
	return err
}
