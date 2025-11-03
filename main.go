package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/url"
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
	ffmpegPath = "/usr/bin/ffmpeg"
	ytDlpPath  = "yt-dlp"
)

var (
	downloadsDir  = "downloads"
	instagramFile = "instagram.txt"
	youtubeFile   = "youtube.txt"
	pinterestFile = "pinterest.txt"
	sem           = make(chan struct{}, 3)
)

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
		if update.Message != nil {
			go handleMessage(bot, update.Message)
		}
	}
}

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

// ===================== MESSAGE HANDLER =====================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	chatID := msg.Chat.ID

	if text == "/start" {
		startMsg := fmt.Sprintf(
			"👋 Salom %s!\n\n🎥 Menga YouTube, Instagram yoki Pinterest link yuboring — men sizga videoni yoki rasmni yuboraman.",
			msg.From.FirstName,
		)
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return
	}
	if text == "/help" {
		helpMsg := "❓ Yordam kerak bo'lsa @nonfindable1 ga murojaat qiling."
		bot.Send(tgbotapi.NewMessage(chatID, helpMsg))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
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

			// Delete loading message
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
				sendMediaAndAttachShareButtons(bot, chatID, file, replyToID, mediaType)
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

// ===================== DOWNLOAD MEDIA =====================
func downloadMedia(url string) ([]string, string, error) {
	start := time.Now()
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	switch {
	case strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be"):
		return downloadYouTube(url, outputTemplate, start)
	case strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am"):
		return downloadInstagram(url, outputTemplate, start)
	case strings.Contains(url, "pinterest.com") || strings.Contains(url, "pin.it"):
		return downloadPinterest(url, outputTemplate, start)
	}

	return nil, "", fmt.Errorf("unsupported link")
}

// ===================== YOUTUBE =====================
func downloadYouTube(url, output string, start time.Time) ([]string, string, error) {
	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--restrict-filenames",
		"--ffmpeg-location", ffmpegPath,
		"-f", "bestvideo[height<=720]+bestaudio/best",
		"--merge-output-format", "mp4",
		"-o", output,
		url,
	}
	if fileExists(youtubeFile) {
		args = append(args, "--cookies", youtubeFile)
	}

	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output:\n%s", out)
	files := filesCreatedAfterRecursive(downloadsDir, start)
	return files, "video", err
}

// ===================== INSTAGRAM =====================
func downloadInstagram(url, output string, start time.Time) ([]string, string, error) {
	args := []string{"--no-warnings", "--ffmpeg-location", ffmpegPath, "-o", output, url}
	if fileExists(instagramFile) {
		args = append(args, "--cookies", instagramFile)
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 Instagram yt-dlp output:\n%s", out)
	if err != nil {
		return nil, "", fmt.Errorf("yt-dlp error: %v", err)
	}

	files := filesCreatedAfterRecursive(downloadsDir, start)
	if len(files) == 0 {
		return nil, "", fmt.Errorf("no files downloaded from Instagram")
	}

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

// ===================== PINTEREST =====================
func downloadPinterest(url, output string, start time.Time) ([]string, string, error) {
	args := []string{"--no-warnings", "--ffmpeg-location", ffmpegPath, "-o", output, url}
	if fileExists(pinterestFile) {
		args = append(args, "--cookies", pinterestFile)
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 Pinterest yt-dlp output:\n%s", out)
	files := filesCreatedAfterRecursive(downloadsDir, start)
	if err == nil && len(files) > 0 {
		return files, "video", nil
	}

	// Fallback: gallery-dl for images
	argsGD := []string{"-d", downloadsDir, url}
	if fileExists(pinterestFile) {
		argsGD = []string{"--cookies", pinterestFile, "-d", downloadsDir, url}
	}
	out, err = runCommandCapture("gallery-dl", argsGD...)
	log.Printf("🖼️ Pinterest gallery-dl output:\n%s", out)
	files = filesCreatedAfterRecursive(downloadsDir, start)
	if err != nil || len(files) == 0 {
		return nil, "", fmt.Errorf("Pinterest download failed: %v", err)
	}

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

func filesCreatedAfterRecursive(dir string, t time.Time) []string {
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
	sort.Slice(res, func(i, j int) bool {
		fi, _ := os.Stat(res[i])
		fj, _ := os.Stat(res[j])
		return fi.ModTime().Before(fj.ModTime())
	})
	return res
}

// ===================== SEND MEDIA WITH SHARE BUTTONS =====================
func sendMediaAndAttachShareButtons(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int, mediaType string) error {
	var sentMsg tgbotapi.Message
	var err error

	caption := "@downloaderin123_bot orqali yuklab olindi"

	// 1️⃣ Send media
	switch mediaType {
	case "video":
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.ReplyToMessageID = replyTo
		video.Caption = caption
		sentMsg, err = bot.Send(video)
	case "image":
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.ReplyToMessageID = replyTo
		photo.Caption = caption
		sentMsg, err = bot.Send(photo)
	default:
		return fmt.Errorf("unknown media type: %s", mediaType)
	}
	if err != nil {
		return fmt.Errorf("failed to send media: %w", err)
	}

	// 2️⃣ Build share & group buttons
	msgLink := fmt.Sprintf("https://t.me/%s/%d", bot.Self.UserName, sentMsg.MessageID)
	shareURL := fmt.Sprintf("https://t.me/share/url?url=%s", url.QueryEscape(msgLink))

	btnShare := tgbotapi.NewInlineKeyboardButtonURL("📤 Do'stlar bilan ulashish", shareURL)
	btnGroup := tgbotapi.NewInlineKeyboardButtonURL(
		"👥 Guruhga qo'shish",
		fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName),
	)

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(btnShare),
		tgbotapi.NewInlineKeyboardRow(btnGroup),
	)

	edit := tgbotapi.NewEditMessageReplyMarkup(chatID, sentMsg.MessageID, keyboard)
	if _, err := bot.Send(edit); err != nil {
		log.Printf("⚠️ Failed to attach keyboard: %v", err)
	}

	return nil
}
