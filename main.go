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
	ffmpegPath = "/usr/bin"
	ytDlpPath  = "yt-dlp"
)

var (
	downloadsDir       = "downloads"
	instaCookiesFile   = "cookies.txt"
	youtubeCookiesFile = "youtube_cookies.txt"
	sem                = make(chan struct{}, 3) // limit concurrent downloads
)

// ===================== HEALTH CHECK SERVER =====================
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
		startMsg := "👋 Salom!\n\n🎥 Menga YouTube, Instagram yoki TikTok link yuboring — men sizga videoni yuboraman.\n\n📸 Endi Instagram Stories va rasmlar (carousel) ham yuklash mumkin!"
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return

	case "/help":
		helpMsg := "ℹ️ Bot haqida:\n\n📹 YouTube, Instagram, TikTok videolarini yuklab beradi.\n📸 Instagram Stories va rasmlarni ham yuklay oladi.\n\nAgar muammo bo‘lsa, bog‘laning: @nonfindable"
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

			// remove loading message
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", url, err)
				errorMsg := tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Link to‘g‘riligini tekshiring.")
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

			if len(files) > 1 {
				sendImagesAsAlbum(bot, chatID, files, replyToID)
			} else if len(files) == 1 {
				sendVideo(bot, chatID, files[0], replyToID)
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

// ===================== DOWNLOAD FUNCTION =====================
func downloadMedia(url string) ([]string, error) {
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))
	isYouTube := strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
	isInstagram := strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am")
	isTikTok := strings.Contains(url, "tiktok.com")

	args := []string{
		"--no-warnings",
		"--restrict-filenames",
		"--merge-output-format", "mp4",
		"--ffmpeg-location", ffmpegPath,
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
		"-o", outputTemplate,
	}

	// =================== COOKIES ===================
	if isYouTube && fileExists(youtubeCookiesFile) {
		args = append(args, "--cookies", youtubeCookiesFile)
		log.Printf("🍪 Using YouTube cookies for %s", url)
	} else if isInstagram && fileExists(instaCookiesFile) {
		args = append(args, "--cookies", instaCookiesFile)
		log.Printf("🍪 Using Instagram cookies for %s", url)
	}

	// =================== FORMAT ===================
	if isYouTube {
		args = append(args, "-f", "bv*[height<=720]+ba/best[height<=720]/best")
	} else if isTikTok {
		args = append(args, "-f", "best")
	} else {
		args = append(args, "-f", "best")
	}

	// =================== STORY FIX ===================
	if isInstagram && strings.Contains(url, "stories/") {
		args = append(args, "--compat-options", "no-youtube-unavailable-videos", "--force-overwrites", "--no-mtime")
	} else {
		args = append(args, "--no-playlist")
	}

	args = append(args, url)

	log.Printf("⚙️ Downloading with yt-dlp: %s", url)
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output:\n%s", out)

	files, _ := filepath.Glob(filepath.Join(downloadsDir, fmt.Sprintf("%d_*.*", uniqueID)))

	// fallback if yt-dlp fails or no files
	if err != nil || len(files) == 0 {
		if isInstagram {
			log.Printf("🖼️ Falling back to gallery-dl for Instagram photos or carousel...")

			galleryDir := filepath.Join(downloadsDir, fmt.Sprintf("%d_gallery", uniqueID))
			os.MkdirAll(galleryDir, 0755)

			galleryArgs := []string{"-d", galleryDir, url}
			out2, err2 := runCommandCapture("gallery-dl", galleryArgs...)
			log.Printf("🖼️ gallery-dl output:\n%s", out2)

			if err2 != nil {
				return nil, fmt.Errorf("gallery-dl failed: %v", err2)
			}

			imgs, _ := filepath.Glob(filepath.Join(galleryDir, "**", "*.jpg"))
			if len(imgs) == 0 {
				imgs, _ = filepath.Glob(filepath.Join(galleryDir, "**", "*.png"))
			}
			if len(imgs) == 0 {
				return nil, fmt.Errorf("no files downloaded by gallery-dl")
			}
			return imgs, nil
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no file found after download")
	}

	log.Printf("✅ Download complete: %s", files[0])
	return files, nil
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

// ===================== SENDER FUNCTIONS =====================
func sendVideo(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	if strings.HasSuffix(filePath, ".jpg") || strings.HasSuffix(filePath, ".png") {
		sendImage(bot, chatID, filePath, replyToMessageID)
		return
	}

	msg := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = "🎥 Video"
	msg.ReplyToMessageID = replyToMessageID

	if _, err := bot.Send(msg); err != nil {
		log.Printf("❌ Failed to send video %s: %v", filePath, err)
		sendDocument(bot, chatID, filePath, replyToMessageID)
	} else {
		os.Remove(filePath)
	}
}

func sendImage(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	msg := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = "📸 Rasm"
	msg.ReplyToMessageID = replyToMessageID
	bot.Send(msg)
	os.Remove(filePath)
}

func sendImagesAsAlbum(bot *tgbotapi.BotAPI, chatID int64, files []string, replyToMessageID int) {
	var mediaGroup []interface{}
	for _, f := range files {
		mediaGroup = append(mediaGroup, tgbotapi.NewInputMediaPhoto(tgbotapi.FilePath(f)))
	}
	album := tgbotapi.NewMediaGroup(chatID, mediaGroup)
	album.ReplyToMessageID = replyToMessageID
	bot.Send(album)
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
