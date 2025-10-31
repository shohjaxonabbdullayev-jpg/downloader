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
	ffmpegPath         = "/usr/bin"
	ytDlpPath          = "yt-dlp"
	instaCookiesFile   = "cookies.txt"
	youtubeCookiesFile = "youtube_cookies.txt"
)

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 3)
)

// ===================== HEALTH CHECK =====================
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

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		return
	}

	for _, link := range links {
		loadingMsg := tgbotapi.NewMessage(msg.Chat.ID, "⏳ Yuklanmoqda... iltimos kuting.")
		loadingMsg.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(loadingMsg)

		go func(url string, chatID int64, replyToID, loadingMsgID int) {
			sem <- struct{}{}
			files, mediaType, err := downloadMedia(url)
			<-sem

			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", url, err)
				errorMsg := tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Linkni tekshiring yoki cookies kerak bo‘lishi mumkin.")
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

			for _, file := range files {
				sendMedia(bot, chatID, file, replyToID, mediaType, url)
			}
		}(link, msg.Chat.ID, msg.MessageID, sent.MessageID)
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

	// YouTube (regular videos + shorts)
	if strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be") {
		files, mediaType, err := downloadYouTube(url, outputTemplate)
		if err != nil {
			return nil, "", err
		}
		return files, mediaType, nil
	}

	// Instagram (posts, reels, stories)
	if strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am") {
		files, mediaType, err := downloadInstagram(url, outputTemplate)
		if err != nil {
			return nil, "", err
		}
		return files, mediaType, nil
	}

	// Pinterest (video pins via yt-dlp, image pins via gallery-dl)
	if strings.Contains(url, "pinterest.com") || strings.Contains(url, "pin.it") {
		files, mediaType, err := downloadPinterest(url, outputTemplate, start)
		if err != nil {
			return nil, "", err
		}
		return files, mediaType, nil
	}

	// Unsupported link
	return nil, "", fmt.Errorf("unsupported link: %s", url)
}

// ===================== YOUTUBE =====================
func downloadYouTube(url, output string) ([]string, string, error) {
	args := []string{"--no-playlist", "--no-warnings", "--restrict-filenames", "--ffmpeg-location", ffmpegPath, "-o", output}
	if fileExists(youtubeCookiesFile) {
		args = append(args, "--cookies", youtubeCookiesFile)
	}
	args = append(args, "-f", "bestvideo[height<=720]+bestaudio/best", "--recode-video", "mp4", "--merge-output-format", "mp4", url)

	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output (YouTube):\n%s", out)
	if err != nil {
		return nil, "", err
	}

	return filesCreatedAfter(downloadsDir, time.Now().Add(-time.Second)), "video", nil
}

// ===================== INSTAGRAM =====================
func downloadInstagram(url, output string) ([]string, string, error) {
	if strings.Contains(url, "/stories/") && !fileExists(instaCookiesFile) {
		return nil, "", fmt.Errorf("cookies.txt required for story download")
	}

	args := []string{"--no-playlist", "--no-warnings", "--restrict-filenames", "--ffmpeg-location", ffmpegPath, "-o", output}
	if fileExists(instaCookiesFile) {
		args = append(args, "--cookies", instaCookiesFile)
	}
	args = append(args, "--recode-video", "mp4", url)

	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output (Instagram):\n%s", out)
	if err != nil {
		return nil, "", err
	}

	return filesCreatedAfter(downloadsDir, time.Now().Add(-time.Second)), "video", nil
}

// ===================== PINTEREST =====================
func downloadPinterest(url, output string, start time.Time) ([]string, string, error) {
	// Try yt-dlp first for video pins
	args := []string{"--no-playlist", "--no-warnings", "--restrict-filenames", "--ffmpeg-location", ffmpegPath, "-o", output, url}
	out, err := runCommandCapture(ytDlpPath, args...)
	if err == nil && len(filesCreatedAfter(downloadsDir, start)) > 0 {
		log.Printf("🧾 yt-dlp output (Pinterest Video):\n%s", out)
		return filesCreatedAfter(downloadsDir, start), "video", nil
	}

	// fallback to gallery-dl for images
	out, err = runCommandCapture("gallery-dl", "-d", downloadsDir, url)
	if err != nil {
		return nil, "", err
	}
	log.Printf("🖼️ gallery-dl output (Pinterest Image):\n%s", out)
	return filesCreatedAfter(downloadsDir, start), "image", nil
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
	var res []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			subfiles := filesCreatedAfter(filepath.Join(dir, e.Name()), t)
			res = append(res, subfiles...)
			continue
		}
		fullPath := filepath.Join(dir, e.Name())
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		if info.ModTime().After(t.Add(-1 * time.Second)) {
			res = append(res, fullPath)
		}
	}
	sort.Slice(res, func(i, j int) bool {
		fi, _ := os.Stat(res[i])
		fj, _ := os.Stat(res[j])
		return fi.ModTime().After(fj.ModTime())
	})
	return res
}

// ===================== SENDERS =====================
func sendMedia(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int, mediaType, url string) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL("📤 Do'stlar bilan ulashish", fmt.Sprintf("https://t.me/share/url?url=%s", url)),
			tgbotapi.NewInlineKeyboardButtonURL("➕ Guruhga qo'shish", fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName)),
		),
	)

	if mediaType == "image" {
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.Caption = "@downloader_bot orqali yuklab olindi"
		photo.ReplyToMessageID = replyToMessageID
		photo.ReplyMarkup = keyboard
		bot.Send(photo)
	} else {
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.Caption = "@downloader_bot orqali yuklab olindi"
		video.ReplyToMessageID = replyToMessageID
		video.ReplyMarkup = keyboard
		bot.Send(video)
	}

	os.Remove(filePath)
}
