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
	ffmpegPath = "/usr/bin" // ffmpeg path for merging
	ytDlpPath  = "yt-dlp"
)

var (
	downloadsDir = "downloads"
	cookiesFile  = "cookies.txt"
	sem          = make(chan struct{}, 3) // limit concurrent downloads
)

func startHealthCheckServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "✅ Bot is healthy and running!")
	})
	log.Printf("💚 Health check server running on port %s", port)
	http.ListenAndServe(":"+port, nil)
}

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

	os.MkdirAll(downloadsDir, 0755)

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Bot init failed: %v", err)
	}
	log.Printf("🤖 Logged in as @%s", bot.Self.UserName)

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

func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	chatID := msg.Chat.ID

	if text == "/start" {
		startMsg := fmt.Sprintf("👋 Salom %s!\n\n🎥 Menga YouTube, Instagram, TikTok yoki Pinterest link yuboring — men sizga videoni yoki rasmni yuboraman.", msg.From.UserName)
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Faqat YouTube, Instagram, TikTok yoki Pinterest link yuboring."))
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

			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", url, err)
				errMsg := tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Linkni tekshiring.")
				errMsg.ReplyToMessageID = replyToID
				bot.Send(errMsg)
				return
			}

			for _, f := range files {
				sendMedia(bot, chatID, f, replyToID, mediaType)
			}
		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

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
		strings.Contains(text, "tiktok.com") ||
		strings.Contains(text, "pin.it") ||
		strings.Contains(text, "pinterest.com")
}

func downloadMedia(url string) ([]string, string, error) {
	start := time.Now()
	output := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", time.Now().UnixNano()))

	switch {
	case strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be"):
		return downloadYouTube(url, output)
	case strings.Contains(url, "tiktok.com"):
		return downloadTikTok(url, output)
	case strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am"):
		return downloadInstagram(url, output, start)
	case strings.Contains(url, "pinterest.com") || strings.Contains(url, "pin.it"):
		return downloadPinterest(url, output, start)
	default:
		return nil, "", fmt.Errorf("unsupported URL")
	}
}

// --------------------- PLATFORM HANDLERS ---------------------

func downloadYouTube(url, output string) ([]string, string, error) {
	args := []string{"--no-playlist", "--no-warnings", "--ffmpeg-location", ffmpegPath, "--cookies", cookiesFile, "-f", "bestvideo+bestaudio/best", "--merge-output-format", "mp4", "-o", output, url}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Println(out)
	if err != nil {
		return nil, "", err
	}
	return filesCreatedAfter(downloadsDir, time.Now().Add(-time.Minute)), "video", nil
}

func downloadTikTok(url, output string) ([]string, string, error) {
	args := []string{"--no-warnings", "--ffmpeg-location", ffmpegPath, "-o", output, url}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Println(out)
	if err != nil {
		return nil, "", err
	}
	return filesCreatedAfter(downloadsDir, time.Now().Add(-time.Minute)), "video", nil
}

func downloadInstagram(url, output string, start time.Time) ([]string, string, error) {
	args := []string{"--no-warnings", "--ffmpeg-location", ffmpegPath, "-o", output, url}
	if fileExists(cookiesFile) {
		args = append(args, "--cookies", cookiesFile)
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 Instagram yt-dlp output:\n%s", out)

	files := filesCreatedAfter(downloadsDir, start)
	if err == nil && len(files) > 0 {
		return files, "video", nil
	}

	// fallback for images and carousels
	out, err = runCommandCapture("gallery-dl", "-d", downloadsDir, url)
	log.Printf("🖼️ Instagram gallery-dl output:\n%s", out)
	if err != nil {
		return nil, "", err
	}
	return filesCreatedAfter(downloadsDir, start), "image", nil
}

func downloadPinterest(url, output string, start time.Time) ([]string, string, error) {
	args := []string{"--no-warnings", "--ffmpeg-location", ffmpegPath, "-o", output, url}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 Pinterest yt-dlp output:\n%s", out)
	if err == nil && len(filesCreatedAfter(downloadsDir, start)) > 0 {
		return filesCreatedAfter(downloadsDir, start), "video", nil
	}

	out, err = runCommandCapture("gallery-dl", "-d", downloadsDir, url)
	log.Printf("🖼️ Pinterest gallery-dl output:\n%s", out)
	if err != nil {
		return nil, "", err
	}
	return filesCreatedAfter(downloadsDir, start), "image", nil
}

// --------------------- HELPERS ---------------------

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func filesCreatedAfter(dir string, t time.Time) []string {
	files, _ := os.ReadDir(dir)
	var result []string
	for _, f := range files {
		if info, err := os.Stat(filepath.Join(dir, f.Name())); err == nil && info.ModTime().After(t) {
			result = append(result, filepath.Join(dir, f.Name()))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		fi, _ := os.Stat(result[i])
		fj, _ := os.Stat(result[j])
		return fi.ModTime().Before(fj.ModTime())
	})
	return result
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
