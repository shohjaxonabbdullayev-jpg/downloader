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

const (
	downloadsDir = "downloads"
)

var (
	youtubeCookies   = "youtube_cookies.txt"
	instagramCookies = "instagram_cookies.txt"
	pinterestCookies = "pinterest_cookies.txt"
)

func main() {
	_ = godotenv.Load()

	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		log.Fatal("BOT_TOKEN not found in .env")
	}

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	log.Printf("🤖 Authorized as @%s", bot.Self.UserName)
	os.MkdirAll(downloadsDir, os.ModePerm)

	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	go http.ListenAndServe(":10000", nil)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil || update.Message.Text == "" {
			continue
		}
		text := strings.TrimSpace(update.Message.Text)

		go func() {
			files, err := downloadMedia(text)
			if err != nil {
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, "❌ Yuklab olishda xatolik yuz berdi. Iltimos, havolani tekshirib qayta urinib ko‘ring.")
				bot.Send(msg)
				return
			}

			for _, file := range files {
				sendMediaAndAttachShareButtons(bot, update.Message.Chat.ID, file)
				os.Remove(file)
			}
		}()
	}
}

// ---------------- DOWNLOAD LOGIC ----------------

func downloadMedia(url string) ([]string, error) {
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	isYouTube := strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
	isInstagram := strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am")
	isPinterest := strings.Contains(url, "pinterest.com") || strings.Contains(url, "pin.it")
	isTikTok := strings.Contains(url, "tiktok.com")

	var cmd *exec.Cmd

	switch {
	case isYouTube:
		if _, err := os.Stat(youtubeCookies); os.IsNotExist(err) {
			if err := fetchCookies("https://www.youtube.com", youtubeCookies); err != nil {
				return nil, fmt.Errorf("failed to fetch youtube cookies: %v", err)
			}
		}
		cmd = exec.Command("yt-dlp", "--cookies", youtubeCookies, "-o", outputTemplate, "-f", "best", url)

	case isInstagram:
		if _, err := os.Stat(instagramCookies); os.IsNotExist(err) {
			if err := fetchCookies("https://www.instagram.com", instagramCookies); err != nil {
				return nil, fmt.Errorf("failed to fetch instagram cookies: %v", err)
			}
		}
		cmd = exec.Command("yt-dlp", "--cookies", instagramCookies, "-o", outputTemplate, "--no-mtime", url)

	case isPinterest:
		if _, err := os.Stat(pinterestCookies); os.IsNotExist(err) {
			if err := fetchCookies("https://www.pinterest.com", pinterestCookies); err != nil {
				return nil, fmt.Errorf("failed to fetch pinterest cookies: %v", err)
			}
		}
		cmd = exec.Command("gallery-dl", "--cookies", pinterestCookies, "-d", downloadsDir, url)

	case isTikTok:
		cmd = exec.Command("yt-dlp", "-o", outputTemplate, url)

	default:
		return nil, fmt.Errorf("unsupported URL")
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

// ---------------- COOKIE FETCHER ----------------

func fetchCookies(url string, filename string) error {
	cmd := exec.Command("yt-dlp", "--cookies-from-browser", "chrome", "--dump-cookies", filename, url)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cannot fetch cookies: %v", err)
	}
	return nil
}

// ---------------- MEDIA SENDER ----------------

func sendMediaAndAttachShareButtons(bot *tgbotapi.BotAPI, chatID int64, filePath string) error {
	caption := "@downloaderin123_bot orqali yuklab olindi"
	shareKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL("🔗 Do‘stlarga ulashish", "https://t.me/downloaderin123_bot"),
		),
	)

	ext := strings.ToLower(filepath.Ext(filePath))
	if strings.Contains(ext, "jpg") || strings.Contains(ext, "jpeg") || strings.Contains(ext, "png") || strings.Contains(ext, "webp") {
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
