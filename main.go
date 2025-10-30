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
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	ffmpegPath   = "/usr/bin"
	ytDlpPath    = "yt-dlp"
	galleryDlBin = "gallery-dl"
)

var (
	downloadsDir       = "downloads"
	instaCookiesFile   = "cookies.txt"
	youtubeCookiesFile = "youtube_cookies.txt"
	sem                = make(chan struct{}, 3)
)

// ===================== HEALTH CHECK =====================
func startHealthCheckServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "✅ Bot is running and healthy!")
	})
	log.Printf("💚 Starting health check server on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Health check server failed: %v", err)
	}
}

// ===================== MAIN =====================
func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env file not found, using system environment variables")
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
		log.Fatalf("❌ Failed to create downloads directory: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Failed to initialize bot: %v", err)
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

	switch text {
	case "/start":
		startMsg := "👋 Salom!\n\n🎥 Menga YouTube, Instagram yoki TikTok link yuboring — men sizga videoni yuboraman.\n\n📸 Endi Instagram Stories va Rasmlarni ham yuklash mumkin!"
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return

	case "/help":
		helpMsg := "ℹ️ Bot haqida:\n\n📹 YouTube, Instagram, TikTok videolarini yuklab beradi.\n📸 Instagram Stories, post va karusellarni ham yuklaydi.\n\nAgar muammo bo‘lsa, bog‘laning: @nonfindable"
		bot.Send(tgbotapi.NewMessage(chatID, helpMsg))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		return
	}

	for _, link := range links {
		if strings.Contains(link, "youtube.com/shorts/") {
			link = strings.Replace(link, "shorts/", "watch?v=", 1)
		}

		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda... iltimos kuting.")
		loadingMsg.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(loadingMsg)

		go func(url string, chatID int64, replyToID, loadingMsgID int) {
			sem <- struct{}{}
			files, err := downloadMedia(url)
			<-sem

			// Remove loading message
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", url, err)
				errorMsg := tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Iltimos, linkning to‘g‘ri ekanligiga ishonch hosil qiling.")
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

			if len(files) > 1 {
				sendImages(bot, chatID, files, replyToID)
			} else if len(files) == 1 {
				sendSingleFile(bot, chatID, files[0], replyToID)
			} else {
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi: fayl topilmadi."))
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
		strings.Contains(text, "tiktok.com")
}

// ===================== DOWNLOAD HANDLER =====================
func downloadMedia(url string) ([]string, error) {
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))
	isInstagram := strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am")

	// Try yt-dlp first
	files, err := downloadWithYtDlp(url, outputTemplate)
	if err == nil && len(files) > 0 && !strings.HasSuffix(files[0], ".json") {
		return files, nil
	}

	// If it's Instagram, fall back to gallery-dl
	if isInstagram {
		log.Println("🖼️ Falling back to gallery-dl for Instagram photos or carousel...")
		files, err := downloadWithGalleryDl(url, uniqueID)
		return files, err
	}

	return nil, fmt.Errorf("no file downloaded")
}

// ===================== YT-DLP DOWNLOAD =====================
func downloadWithYtDlp(url, outputTemplate string) ([]string, error) {
	args := []string{
		"--no-warnings",
		"--restrict-filenames",
		"--merge-output-format", "mp4",
		"--ffmpeg-location", ffmpegPath,
		"--user-agent", "Mozilla/5.0",
		"-o", outputTemplate,
		url,
	}

	if strings.Contains(url, "instagram.com") && fileExists(instaCookiesFile) {
		args = append([]string{"--cookies", instaCookiesFile}, args...)
	}

	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output:\n%s", out)

	files, _ := filepath.Glob(strings.Replace(outputTemplate, "%(title)s.%(ext)s", "*.*", 1))
	if len(files) == 0 {
		return nil, err
	}

	return files, err
}

// ===================== GALLERY-DL DOWNLOAD =====================
func downloadWithGalleryDl(url string, uniqueID int64) ([]string, error) {
	targetDir := filepath.Join(downloadsDir, fmt.Sprintf("%d_gallery", uniqueID))
	os.MkdirAll(targetDir, 0755)

	args := []string{
		"-d", targetDir,
	}

	if fileExists(instaCookiesFile) {
		args = append(args, "--cookies", instaCookiesFile)
	}

	args = append(args, url)

	out, err := runCommandCapture(galleryDlBin, args...)
	log.Printf("🖼️ gallery-dl output:\n%s", out)

	files, _ := filepath.Glob(filepath.Join(targetDir, "*"))
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found after gallery-dl")
	}

	return files, err
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

// ===================== SENDERS =====================
func sendSingleFile(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == ".mp4" {
		sendVideo(bot, chatID, filePath, replyToMessageID)
	} else {
		sendPhoto(bot, chatID, filePath, replyToMessageID)
	}
}

func sendVideo(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	msg := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = "🎥 Video"
	msg.ReplyToMessageID = replyToMessageID
	if _, err := bot.Send(msg); err != nil {
		sendDocument(bot, chatID, filePath, replyToMessageID)
	} else {
		os.Remove(filePath)
	}
}

func sendPhoto(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	msg := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = "📸 Rasm"
	msg.ReplyToMessageID = replyToMessageID
	bot.Send(msg)
	os.Remove(filePath)
}

func sendImages(bot *tgbotapi.BotAPI, chatID int64, files []string, replyToMessageID int) {
	var photos []interface{}
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f), ".jpg") ||
			strings.HasSuffix(strings.ToLower(f), ".jpeg") ||
			strings.HasSuffix(strings.ToLower(f), ".png") {
			photos = append(photos, tgbotapi.NewInputMediaPhoto(tgbotapi.FilePath(f)))
		}
	}
	if len(photos) == 0 {
		return
	}
	mediaGroup := tgbotapi.NewMediaGroup(chatID, photos)
	bot.SendMediaGroup(mediaGroup)
	for _, f := range files {
		os.Remove(f)
	}
}

func sendDocument(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
	doc.Caption = "⚠️ Fayl hajmi katta bo‘lgani uchun hujjat sifatida yuborildi."
	doc.ReplyToMessageID = replyToMessageID
	bot.Send(doc)
	os.Remove(filePath)
}
