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
		log.Fatal("BOT_TOKEN missing in .env")
	}

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("🤖 Authorized as @%s", bot.Self.UserName)
	os.MkdirAll(downloadsDir, os.ModePerm)

	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	go http.ListenAndServe(":10000", nil)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil || update.Message.Text == "" {
			continue
		}

		go func(text string, chatID int64) {
			files, err := downloadMedia(text)
			if err != nil {
				msg := tgbotapi.NewMessage(chatID, "❌ Yuklab olishda xatolik. Iltimos, havolani tekshirib qayta urinib ko‘ring.")
				bot.Send(msg)
				return
			}

			for _, f := range files {
				sendMediaAndAttachShareButtons(bot, chatID, f)
				os.Remove(f)
			}
		}(update.Message.Text, update.Message.Chat.ID)
	}
}

func downloadMedia(url string) ([]string, error) {
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	isYouTube := strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
	isInstagram := strings.Contains(url, "instagram.com")
	isPinterest := strings.Contains(url, "pinterest.com")
	isTikTok := strings.Contains(url, "tiktok.com")

	var cmd *exec.Cmd

	switch {
	case isYouTube:
		cmd = exec.Command("yt-dlp", "--cookies", "youtube_cookies.txt", "-o", outputTemplate, "-f", "best", url)

	case isInstagram:
		cmd = exec.Command("yt-dlp", "--cookies", "instagram_cookies.txt", "-o", outputTemplate, "--no-mtime", url)

	case isPinterest:
		cmd = exec.Command("gallery-dl", "--cookies", "pinterest_cookies.txt", "-d", downloadsDir, url)

	case isTikTok:
		cmd = exec.Command("yt-dlp", "-o", outputTemplate, url)

	default:
		return nil, fmt.Errorf("unsupported link")
	}

	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("download failed: %v", err)
	}

	files, err := filepath.Glob(fmt.Sprintf("%s/%d_*", downloadsDir, uniqueID))
	if err != nil || len(files) == 0 {
		return nil, fmt.Errorf("no files found")
	}
	return files, nil
}

func sendMediaAndAttachShareButtons(bot *tgbotapi.BotAPI, chatID int64, filePath string) error {
	caption := "@downloaderin123_bot orqali yuklab olindi"
	shareKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL("🔗 Do‘stlarga ulashish", "https://t.me/downloaderin123_bot"),
		),
	)

	ext := strings.ToLower(filepath.Ext(filePath))
	if strings.Contains(ext, ".jpg") || strings.Contains(ext, ".png") || strings.Contains(ext, ".webp") {
		msg := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		msg.Caption = caption
		msg.ReplyMarkup = shareKeyboard
		_, err := bot.Send(msg)
		return err
	}

	msg := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = caption
	msg.ReplyMarkup = shareKeyboard
	_, err := bot.Send(msg)
	return err
}
