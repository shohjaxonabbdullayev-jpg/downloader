package main

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	"golang.org/x/image/webp"
)

const (
	commandTimeout   = 3 * time.Minute
	defaultDownloads = "downloads"
	telegramMaxSize  = 50 * 1024 * 1024 // 50 MB
)

var (
	sem = make(chan struct{}, 3) // Limit concurrent downloads
)

func main() {
	_ = godotenv.Load()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("BOT_TOKEN not set in environment")
	}

	downloadsDir := getenv("DOWNLOADS_DIR", defaultDownloads)
	if err := os.MkdirAll(downloadsDir, 0o755); err != nil {
		log.Fatalf("Failed to create downloads folder: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("Bot init failed: %v", err)
	}
	log.Printf("✅ Bot authorized as @%s", bot.Self.UserName)

	// Start health server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // default port
	}
	fmt.Println("Server running on port:", port)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			go handleMessage(bot, update.Message, downloadsDir)
		}
	}
}

// ---------------------- Health Server ----------------------
func startHealthServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	go func() {
		log.Printf("✅ Health server running on port %s", port)
		log.Fatal(http.ListenAndServe(":"+port, nil))
	}()
}

// ---------------------- Bot Handlers ----------------------
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, downloadsDir string) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	chatID := msg.Chat.ID

	if text == "/start" {
		bot.Send(tgbotapi.NewMessage(chatID,
			"👋 Salom!\n\n🎥 YouTube / TikTok / Instagram havolasini yuboring — men sizga video yoki rasm yuboraman."))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		return
	}

	for _, link := range links {
		link = normalizeYouTubeShort(link)

		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda...")
		loadingMsg.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(loadingMsg)

		go func(url string, chatID int64, replyToID, loadingMsgID int) {
			sem <- struct{}{}
			files, fileType, cleanup, err := downloadURL(url, downloadsDir)
			<-sem

			defer cleanup()

			bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: loadingMsgID})

			if err != nil {
				log.Printf("Download error for %s: %v", url, err)
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi."))
				return
			}

			for _, filePath := range files {
				sendFile(bot, chatID, filePath, fileType, replyToID)
				_ = os.Remove(filePath)
			}
		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

// ---------------------- Utilities ----------------------
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
		strings.Contains(text, "tiktok.com") ||
		strings.Contains(text, "instagram.com") ||
		strings.Contains(text, "instagr.am")
}

func normalizeYouTubeShort(link string) string {
	if strings.Contains(link, "youtube.com/shorts/") {
		return strings.Replace(link, "shorts/", "watch?v=", 1)
	}
	return link
}

// ---------------------- Download ----------------------
func downloadURL(url, downloadsDir string) ([]string, string, func(), error) {
	uniqueID := time.Now().UnixNano()
	tmpDir := filepath.Join(downloadsDir, fmt.Sprintf("%d", uniqueID))

	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, "", func() {}, fmt.Errorf("tmp dir create: %w", err)
	}

	ffmpegPath := os.Getenv("FFMPEG_PATH")
	ytDlpPath := getenv("YTDLP_PATH", "yt-dlp")

	args := []string{
		"--no-playlist",
		"-o", filepath.Join(tmpDir, "%(title)s.%(ext)s"),
		url,
	}
	if ffmpegPath != "" {
		args = append([]string{"--ffmpeg-location", ffmpegPath}, args...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	out, err := runCommandCapture(ctx, ytDlpPath, args...)
	log.Printf("yt-dlp output: %s", out)
	if err != nil {
		return nil, "", func() { _ = os.RemoveAll(tmpDir) }, fmt.Errorf("yt-dlp failed: %w", err)
	}

	files, _ := filepath.Glob(filepath.Join(tmpDir, "*"))
	if len(files) == 0 {
		return nil, "", func() { _ = os.RemoveAll(tmpDir) }, fmt.Errorf("no media found")
	}

	imageExts := []string{".jpg", ".jpeg", ".png", ".webp"}
	fileType := "video"
	if isAllImages(files, imageExts) {
		fileType = "photo"
	}

	for i, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".webp" {
			jpgPath, convErr := convertWebPToJPG(f)
			if convErr != nil {
				log.Printf("webp convert failed: %v", convErr)
				fileType = "document"
				continue
			}
			_ = os.Remove(f)
			files[i] = jpgPath
		}
	}

	return files, fileType, func() { _ = os.RemoveAll(tmpDir) }, nil
}

func isAllImages(files []string, imageExts []string) bool {
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		found := false
		for _, e := range imageExts {
			if ext == e {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func runCommandCapture(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

// ---------------------- Send files ----------------------
func sendFile(bot *tgbotapi.BotAPI, chatID int64, path, fileType string, replyToMessageID int) {
	info, err := os.Stat(path)
	if err != nil {
		log.Printf("File stat error: %v", err)
		return
	}

	switch fileType {
	case "video":
		if info.Size() > telegramMaxSize {
			doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(path))
			doc.Caption = "🎥 Mana videongiz!"
			doc.ReplyToMessageID = replyToMessageID
			bot.Send(doc)
			return
		}
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(path))
		video.Caption = "🎥 Mana videongiz!"
		video.ReplyToMessageID = replyToMessageID
		if _, err := bot.Send(video); err != nil {
			doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(path))
			doc.Caption = "🎥 Mana videongiz!"
			doc.ReplyToMessageID = replyToMessageID
			bot.Send(doc)
		}
	case "photo":
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(path))
		photo.Caption = "📷 Mana rasm!"
		photo.ReplyToMessageID = replyToMessageID
		bot.Send(photo)
	default:
		doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(path))
		doc.Caption = "📎 Mana fayl!"
		doc.ReplyToMessageID = replyToMessageID
		bot.Send(doc)
	}
}

// ---------------------- WebP to JPG ----------------------
func convertWebPToJPG(src string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()

	img, err := webp.Decode(in)
	if err != nil {
		return "", err
	}

	dst := strings.TrimSuffix(src, filepath.Ext(src)) + ".jpg"
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer out.Close()

	if err := jpeg.Encode(out, img, &jpeg.Options{Quality: 92}); err != nil {
		return "", err
	}

	return dst, nil
}

// ---------------------- Env Helper ----------------------
func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
