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
	ffmpegPath = "/usr/bin" // path to ffmpeg
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
		startMsg := fmt.Sprintf("👋 Salom %s!\n\n🎥 Menga YouTube, Instagram yoki Pinterest link yuboring — men sizga videoni yoki rasmni yuboraman.", msg.From.UserName)
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Iltimos, YouTube, Instagram yoki Pinterest link yuboring."))
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
				errorMsg := tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Linkni tekshiring.")
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

			for _, file := range files {
				sendMedia(bot, chatID, file, replyToID, mediaType)
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
		strings.Contains(text, "pin.it")
}

// ===================== DOWNLOAD FUNCTION =====================
func downloadMedia(url string) ([]string, string, error) {
	start := time.Now()
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	if strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be") {
		return downloadYouTube(url, outputTemplate)
	} else if strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am") {
		return downloadInstagram(url, outputTemplate)
	} else if strings.Contains(url, "pinterest.com") || strings.Contains(url, "pin.it") {
		return downloadPinterest(url, outputTemplate, start)
	}

	return nil, "", fmt.Errorf("unsupported link")
}

// ===================== YOUTUBE =====================
func downloadYouTube(url, output string) ([]string, string, error) {
	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--restrict-filenames",
		"--ffmpeg-location", ffmpegPath,
		"--cookies", cookiesFile,
		"-f", "bestvideo[height<=720]+bestaudio/best",
		"--merge-output-format", "mp4",
		"-o", output,
		url,
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output:\n%s", out)
	if err != nil {
		return nil, "", err
	}
	files := filesCreatedAfter(downloadsDir, time.Now().Add(-time.Minute))
	return files, "video", nil
}

// ===================== INSTAGRAM =====================
func downloadInstagram(url, output string) ([]string, string, error) {
	args := []string{
		"--no-warnings",
		"--ffmpeg-location", ffmpegPath,
		"-o", output,
		url,
	}
	if fileExists(cookiesFile) {
		args = append(args, "--cookies", cookiesFile)
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output:\n%s", out)
	if err != nil {
		return nil, "", err
	}
	files := filesCreatedAfter(downloadsDir, time.Now().Add(-time.Minute))
	return files, "video", nil
}

// ===================== PINTEREST =====================
func downloadPinterest(url, output string, start time.Time) ([]string, string, error) {
	// First try yt-dlp (some Pinterest links are videos)
	args := []string{
		"--no-warnings",
		"--ffmpeg-location", ffmpegPath,
		"-o", output,
		url,
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 Pinterest yt-dlp output:\n%s", out)
	if err == nil && len(filesCreatedAfter(downloadsDir, start)) > 0 {
		files := filesCreatedAfter(downloadsDir, start)
		return files, "video", nil
	}

	// Otherwise fallback to gallery-dl for images
	out, err = runCommandCapture("gallery-dl", "-d", downloadsDir, url)
	log.Printf("🖼️ Pinterest gallery-dl output:\n%s", out)
	if err != nil {
		return nil, "", err
	}
	files := filesCreatedAfter(downloadsDir, start)
	return files, "image", nil
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

func filesCreatedAfter(dir string, t time.Time) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var res []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fullPath := filepath.Join(dir, e.Name())
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		if info.ModTime().After(t) {
			res = append(res, fullPath)
		}
	}
	sort.Slice(res, func(i, j int) bool {
		fi, _ := os.Stat(res[i])
		fj, _ := os.Stat(res[j])
		return fi.ModTime().Before(fj.ModTime())
	})
	return res
}

func sendMedia(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int, mediaType string) {
	switch mediaType {
	case "video":
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.ReplyToMessageID = replyTo
		bot.Send(video)
	case "image":
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.ReplyToMessageID = replyTo
		bot.Send(photo)
	}
}
