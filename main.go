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

var sem = make(chan struct{}, 3) // concurrent download limit

func main() {
	_ = godotenv.Load()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN not set in environment")
	}

	downloadsDir := getenv("DOWNLOADS_DIR", defaultDownloads)
	if err := os.MkdirAll(downloadsDir, 0o755); err != nil {
		log.Fatalf("Failed to create downloads dir: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("Bot init failed: %v", err)
	}
	log.Printf("✅ Bot authorized as @%s", bot.Self.UserName)

	port := getenv("PORT", "10000")
	startHealthServer(port)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			go handleMessage(bot, update.Message, downloadsDir)
		}
	}
}

// ---------------------- HEALTH SERVER ----------------------
func startHealthServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	go func() {
		log.Printf("🌐 Health server running on port %s", port)
		log.Fatal(http.ListenAndServe(":"+port, nil))
	}()
}

// ---------------------- MESSAGE HANDLER ----------------------
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, downloadsDir string) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	chatID := msg.Chat.ID

	if text == "/start" {
		startMsg := "👋 Salom!\n🎥 Yuboring YouTube / TikTok / Instagram havolasini — men sizga video yoki rasm yuboraman."
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Faqat YouTube, Instagram yoki TikTok havolasini yuboring."))
		return
	}

	for _, link := range links {
		link = normalizeYouTubeShort(link)

		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda...")
		loadingMsg.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(loadingMsg)

		go func(url string, chatID int64, replyTo, loadingID int) {
			sem <- struct{}{}
			files, fileType, cleanup, err := downloadURL(url, downloadsDir)
			<-sem
			defer cleanup()

			bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: loadingID})

			if err != nil {
				log.Printf("Download error: %v", err)
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Qayta urinib ko‘ring."))
				return
			}

			for _, f := range files {
				sendMedia(bot, chatID, replyTo, f, fileType)
				_ = os.Remove(f)
			}
		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

// ---------------------- LINK HELPERS ----------------------
func extractSupportedLinks(text string) []string {
	re := regexp.MustCompile(`https?://[^\s]+`)
	all := re.FindAllString(text, -1)
	var valid []string
	for _, l := range all {
		if isSupported(l) {
			valid = append(valid, l)
		}
	}
	return valid
}

func isSupported(link string) bool {
	l := strings.ToLower(link)
	return strings.Contains(l, "youtube.com") ||
		strings.Contains(l, "youtu.be") ||
		strings.Contains(l, "instagram.com") ||
		strings.Contains(l, "instagr.am") ||
		strings.Contains(l, "tiktok.com")
}

func normalizeYouTubeShort(link string) string {
	if strings.Contains(link, "youtube.com/shorts/") {
		return strings.Replace(link, "shorts/", "watch?v=", 1)
	}
	return link
}

// ---------------------- DOWNLOADER ----------------------
func downloadURL(url, downloadsDir string) ([]string, string, func(), error) {
	uid := fmt.Sprintf("%d", time.Now().UnixNano())
	tmpDir := filepath.Join(downloadsDir, uid)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, "", func() {}, err
	}

	ffmpegPath := os.Getenv("FFMPEG_PATH")
	ytDlpPath := getenv("YTDLP_PATH", "yt-dlp")

	args := []string{"--no-playlist", "-o", filepath.Join(tmpDir, "%(title)s.%(ext)s"), url}
	if ffmpegPath != "" {
		args = append([]string{"--ffmpeg-location", ffmpegPath}, args...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	out, err := runCommand(ctx, ytDlpPath, args...)
	log.Println(out)
	if err != nil {
		return nil, "", func() { os.RemoveAll(tmpDir) }, fmt.Errorf("yt-dlp failed: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(tmpDir, "*"))
	if len(files) == 0 {
		return nil, "", func() { os.RemoveAll(tmpDir) }, fmt.Errorf("no files downloaded")
	}

	fileType := "video"
	if isAllImages(files) {
		fileType = "photo"
	}

	for i, f := range files {
		if strings.HasSuffix(strings.ToLower(f), ".webp") {
			jpg, err := convertWebPToJPG(f)
			if err == nil {
				files[i] = jpg
				os.Remove(f)
			}
		}
	}

	return files, fileType, func() { os.RemoveAll(tmpDir) }, nil
}

func runCommand(ctx context.Context, cmd string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, cmd, args...)
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	err := c.Run()
	return buf.String(), err
}

func isAllImages(files []string) bool {
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f)) {
		case ".jpg", ".jpeg", ".png", ".webp":
			continue
		default:
			return false
		}
	}
	return true
}

// ---------------------- SENDER ----------------------
func sendMedia(bot *tgbotapi.BotAPI, chatID int64, replyTo int, filePath string, fileType string) {
	info, err := os.Stat(filePath)
	if err != nil {
		log.Printf("File stat error: %v", err)
		return
	}

	switch fileType {
	case "video":
		if info.Size() > telegramMaxSize {
			doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
			doc.Caption = "🎥 Mana videongiz!"
			doc.ReplyToMessageID = replyTo
			bot.Send(doc)
		} else {
			video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
			video.Caption = "🎥 Mana videongiz!"
			video.ReplyToMessageID = replyTo
			bot.Send(video)
		}
	case "photo":
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath)) // ✅ fixed call
		photo.Caption = "📷 Mana rasm!"
		photo.ReplyToMessageID = replyTo
		bot.Send(photo)
	default:
		doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
		doc.Caption = "📎 Mana fayl!"
		doc.ReplyToMessageID = replyTo
		bot.Send(doc)
	}
	log.Printf("✅ Sent file: %s", filePath)
}

// ---------------------- CONVERT WEBP TO JPG ----------------------
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

// ---------------------- ENV HELPER ----------------------
func getenv(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return fallback
}
