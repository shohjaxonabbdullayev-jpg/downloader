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
	ffmpegPath = "/usr/bin" // path to ffmpeg in container; adjust if needed locally
	ytDlpPath  = "yt-dlp"
)

var (
	downloadsDir = "downloads"
	cookiesFile  = "cookies.txt"
	sem          = make(chan struct{}, 3) // concurrent downloads limit
)

// ===================== HEALTH CHECK SERVER =====================
func startHealthCheckServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "✅ Bot is running and healthy!")
	})
	log.Printf("💚 Health check server running on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Health check server failed: %v", err)
	}
}

// ===================== MAIN =====================
func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env file not found, using system environment")
	}

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN not set")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	go startHealthCheckServer(port)

	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		log.Fatalf("❌ Failed to create downloads folder: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Bot initialization failed: %v", err)
	}

	log.Printf("🤖 Bot authorized as @%s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}
		go handleMessage(bot, update.Message)
	}
}

// ===================== MESSAGE HANDLER =====================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	chatID := msg.Chat.ID

	if text == "/start" {
		startMsg := fmt.Sprintf("👋 Salom %s!\n\n🎥 Menga YouTube, Instagram, Pinterest yoki TikTok link yuboring — men sizga videoni yoki rasmni yuboraman.", msg.From.UserName)
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		// don't reply if no supported links
		return
	}

	for _, link := range links {
		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda... iltimos kuting.")
		loadingMsg.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(loadingMsg)

		go func(url string, chatID int64, replyToID, loadingMsgID int) {
			sem <- struct{}{}
			files, mediaType, err := downloadMedia(url)
			<-sem

			// remove loading message
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", url, err)
				errorMsg := tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ Yuklab bo‘lmadi: %v", err))
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

			for _, file := range files {
				sendMedia(bot, chatID, file, replyToID, mediaType, url)
			}
		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

// ===================== LINK EXTRACTION =====================
func extractSupportedLinks(text string) []string {
	regex := `(https?://[^\s]+)`
	matches := regexp.MustCompile(regex).FindAllString(text, -1)
	var links []string
	for _, m := range matches {
		// normalize trailing punctuation
		m = strings.TrimRight(m, ".,;:!)\"'")
		if isSupportedLink(m) {
			links = append(links, m)
		}
	}
	return links
}

func isSupportedLink(text string) bool {
	text = strings.ToLower(text)
	return strings.Contains(text, "youtube.com") ||
		strings.Contains(text, "youtu.be") ||
		strings.Contains(text, "instagram.com") ||
		strings.Contains(text, "instagr.am") ||
		strings.Contains(text, "pinterest.com") ||
		strings.Contains(text, "pin.it") ||
		strings.Contains(text, "tiktok.com") ||
		strings.Contains(text, "vm.tiktok.com")
}

// ===================== DOWNLOAD FUNCTION =====================
func downloadMedia(url string) ([]string, string, error) {
	start := time.Now()
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	switch {
	case strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be"):
		return downloadYouTube(url, outputTemplate)
	case strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am"):
		return downloadInstagram(url, outputTemplate, start)
	case strings.Contains(url, "pinterest.com") || strings.Contains(url, "pin.it"):
		return downloadPinterest(url, outputTemplate, start)
	case strings.Contains(url, "tiktok.com") || strings.Contains(url, "vm.tiktok.com"):
		return downloadTikTok(url, outputTemplate)
	default:
		return nil, "", fmt.Errorf("unsupported link")
	}
}

// ===================== YOUTUBE =====================
func downloadYouTube(url, output string) ([]string, string, error) {
	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--restrict-filenames",
		"--ffmpeg-location", ffmpegPath,
		"-o", output,
	}
	// prefer cookies if available
	if fileExists(cookiesFile) {
		args = append(args, "--cookies", cookiesFile)
	}
	// format selection and merge
	args = append(args, "-f", "bestvideo[height<=720]+bestaudio/best", "--merge-output-format", "mp4", url)

	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output (YouTube):\n%s", out)
	if err != nil {
		// surface helpful message for login-required errors
		if strings.Contains(out, "Sign in to confirm") || strings.Contains(out, "This video is unavailable") {
			return nil, "", fmt.Errorf("YouTube requires login or the video is restricted; ensure cookies.txt is valid")
		}
		return nil, "", err
	}

	files := filesCreatedAfter(downloadsDir, startMinus(10*time.Second))
	if len(files) == 0 {
		return nil, "", fmt.Errorf("no file produced for YouTube link")
	}
	return files, "video", nil
}

// ===================== INSTAGRAM =====================
// For Instagram: if link looks like a single post/reel/story use yt-dlp (video).
// Otherwise (profile, post with images, gallery) use gallery-dl for images.
func downloadInstagram(url, output string, start time.Time) ([]string, string, error) {
	// If it's a direct item likely containing video: /reel/ /p/ /stories/
	if strings.Contains(url, "/reel/") || strings.Contains(url, "/p/") || strings.Contains(url, "/stories/") {
		args := []string{
			"--no-warnings",
			"--ffmpeg-location", ffmpegPath,
			"-o", output,
		}
		if fileExists(cookiesFile) {
			args = append(args, "--cookies", cookiesFile)
		}
		args = append(args, url)

		out, err := runCommandCapture(ytDlpPath, args...)
		log.Printf("🧾 yt-dlp output (Instagram video):\n%s", out)
		if err != nil {
			return nil, "", err
		}
		files := filesCreatedAfter(downloadsDir, startMinus(10*time.Second))
		if len(files) == 0 {
			return nil, "", fmt.Errorf("no Instagram video produced")
		}
		return files, "video", nil
	}

	// fallback to gallery-dl for images (profile, post with images, collections)
	out, err := runCommandCapture("gallery-dl", "-d", downloadsDir, url)
	log.Printf("🖼️ gallery-dl output (Instagram images):\n%s", out)
	if err != nil {
		return nil, "", err
	}
	files := filesCreatedAfter(downloadsDir, startMinus(10*time.Second))
	if len(files) == 0 {
		return nil, "", fmt.Errorf("no Instagram images produced")
	}
	return files, "image", nil
}

// ===================== PINTEREST =====================
func downloadPinterest(url, output string, start time.Time) ([]string, string, error) {
	// try yt-dlp first (video pins)
	args := []string{
		"--no-warnings",
		"--ffmpeg-location", ffmpegPath,
		"-o", output,
		url,
	}
	if fileExists(cookiesFile) {
		args = append([]string{"--cookies", cookiesFile}, args...)
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output (Pinterest):\n%s", out)
	if err == nil && len(filesCreatedAfter(downloadsDir, startMinus(10*time.Second))) > 0 {
		files := filesCreatedAfter(downloadsDir, startMinus(10*time.Second))
		return files, "video", nil
	}

	// fallback to gallery-dl for images
	out, err = runCommandCapture("gallery-dl", "-d", downloadsDir, url)
	log.Printf("🖼️ gallery-dl output (Pinterest images):\n%s", out)
	if err != nil {
		return nil, "", err
	}
	files := filesCreatedAfter(downloadsDir, startMinus(10*time.Second))
	if len(files) == 0 {
		return nil, "", fmt.Errorf("no Pinterest media produced")
	}
	return files, "image", nil
}

// ===================== TIKTOK =====================
func downloadTikTok(url, output string) ([]string, string, error) {
	args := []string{
		"--no-warnings",
		"--ffmpeg-location", ffmpegPath,
		"-o", output,
		url,
	}
	if fileExists(cookiesFile) {
		args = append([]string{"--cookies", cookiesFile}, args...)
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output (TikTok):\n%s", out)
	if err != nil {
		return nil, "", err
	}
	files := filesCreatedAfter(downloadsDir, startMinus(10*time.Second))
	if len(files) == 0 {
		return nil, "", fmt.Errorf("no TikTok file produced")
	}
	return files, "video", nil
}

// ===================== HELPERS =====================

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

// filesCreatedAfter walks downloadsDir recursively and returns files
// whose ModTime is after provided time t. Results are sorted newest first.
func filesCreatedAfter(dir string, t time.Time) []string {
	var res []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// ignore unreadable files
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if info.ModTime().After(t) {
			res = append(res, path)
		}
		return nil
	})
	sort.Slice(res, func(i, j int) bool {
		fi, _ := os.Stat(res[i])
		fj, _ := os.Stat(res[j])
		return fi.ModTime().After(fj.ModTime()) // newest first
	})
	return res
}

// small helper to get start - d duration safely
func startMinus(d time.Duration) time.Time {
	return time.Now().Add(-d)
}

// ===================== TELEGRAM SENDER =====================
func sendMedia(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int, mediaType, sourceURL string) {
	buttonShare := tgbotapi.NewInlineKeyboardButtonURL("📤 Do'stlar bilan ulashish", fmt.Sprintf("https://t.me/share/url?url=%s", sourceURL))
	buttonGroup := tgbotapi.NewInlineKeyboardButtonURL("➕ Guruhga qo'shish", fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName))
	keyboard := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(buttonShare, buttonGroup))

	// choose how to send
	if mediaType == "image" {
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.Caption = "@downloader_bot orqali yuklab olindi"
		photo.ReplyToMessageID = replyTo
		photo.ReplyMarkup = keyboard
		if _, err := bot.Send(photo); err != nil {
			log.Printf("❌ Failed to send photo %s: %v", filePath, err)
		}
	} else {
		// always send as streaming video
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.Caption = "@downloader_bot orqali yuklab olindi"
		video.ReplyToMessageID = replyTo
		video.ReplyMarkup = keyboard
		if _, err := bot.Send(video); err != nil {
			log.Printf("❌ Failed to send video %s: %v", filePath, err)
		}
	}

	// cleanup local file
	if err := os.Remove(filePath); err != nil {
		log.Printf("⚠️ Failed to remove file %s: %v", filePath, err)
	}
}
