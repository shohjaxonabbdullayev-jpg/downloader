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
	downloadsDir         = "downloads"
	instaCookiesFile     = "cookies.txt"
	youtubeCookiesFile   = "youtube_cookies.txt"
	pinterestCookiesFile = "pinterest_cookies.txt"
	sem                  = make(chan struct{}, 3)
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
		log.Fatal("❌ BOT_TOKEN not set in environment")
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
		startMsg := "👋 Salom!\n\n🎥 Menga YouTube, Instagram, TikTok yoki Pinterest link yuboring — men sizga videoni yuboraman."
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
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

		if strings.Contains(link, "pin.it") {
			link = resolveShortLink(link)
		}

		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda... iltimos kuting.")
		loadingMsg.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(loadingMsg)

		go func(url string, chatID int64, replyToID, loadingMsgID int) {
			sem <- struct{}{}
			files, err := downloadMedia(url)
			<-sem

			// delete "loading..." message
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", url, err)
				errorMsg := tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Iltimos, link to‘g‘riligini tekshiring.")
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

			if len(files) == 0 {
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Fayl topilmadi."))
				return
			}

			// Decide whether it's image or video
			if strings.HasSuffix(files[0], ".jpg") || strings.HasSuffix(files[0], ".png") || strings.HasSuffix(files[0], ".jpeg") {
				sendPhoto(bot, chatID, files[0], replyToID)
			} else {
				sendVideoWithButton(bot, chatID, files[0], replyToID)
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
		strings.Contains(text, "tiktok.com") ||
		strings.Contains(text, "pinterest.com") ||
		strings.Contains(text, "pin.it")
}

// ===================== SHORT LINK RESOLVER =====================
func resolveShortLink(url string) string {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return nil },
		Timeout:       10 * time.Second,
	}
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("⚠️ Failed to resolve short link %s: %v", url, err)
		return url
	}
	defer resp.Body.Close()
	finalURL := resp.Request.URL.String()
	if finalURL != url {
		log.Printf("🔗 Resolved short link: %s → %s", url, finalURL)
	}
	return finalURL
}

// ===================== DOWNLOAD FUNCTION =====================
func downloadMedia(url string) ([]string, error) {
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	isYouTube := strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
	isInstagram := strings.Contains(url, "instagram.com")
	isPinterest := strings.Contains(url, "pinterest.com")

	args := []string{
		"--no-warnings",
		"--restrict-filenames",
		"--merge-output-format", "mp4",
		"--ffmpeg-location", ffmpegPath,
		"--no-check-certificates",
		"-o", outputTemplate,
	}

	if isYouTube && fileExists(youtubeCookiesFile) {
		args = append(args, "--cookies", youtubeCookiesFile)
	} else if isInstagram && fileExists(instaCookiesFile) {
		args = append(args, "--cookies", instaCookiesFile)
	} else if isPinterest && fileExists(pinterestCookiesFile) {
		args = append(args, "--cookies", pinterestCookiesFile)
	}

	if isYouTube {
		args = append(args, "-f", "bv*[height<=720]+ba/best[height<=720]/best")
	} else {
		args = append(args, "-f", "best")
	}

	args = append(args, url)

	log.Printf("⚙️ Downloading with yt-dlp: %s", url)
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output:\n%s", out)

	files, _ := filepath.Glob(filepath.Join(downloadsDir, fmt.Sprintf("%d_*.*", uniqueID)))
	if len(files) == 0 {
		log.Printf("⚠️ yt-dlp didn’t find any files.")
	}

	// fallback: Instagram image or story (gallery-dl)
	if (err != nil || len(files) == 0) && isInstagram {
		log.Printf("⚠️ Trying gallery-dl for Instagram...")
		gArgs := []string{"-d", downloadsDir, url}
		out2, gErr := runCommandCapture("gallery-dl", gArgs...)
		log.Printf("🖼️ gallery-dl output:\n%s", out2)
		if gErr == nil {
			files, _ = filepath.Glob(filepath.Join(downloadsDir, "*"))
			if len(files) > 0 {
				return files, nil
			}
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no file downloaded for %s", url)
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

// ===================== SENDERS =====================
func sendVideoWithButton(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int) {
	video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	video.Caption = "@downloaderin123_bot orqali yuklab olindi"
	video.ReplyToMessageID = replyTo

	button := tgbotapi.NewInlineKeyboardButtonURL("➕ Guruhga qo‘shish", fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName))
	keyboard := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(button))
	video.ReplyMarkup = keyboard

	if _, err := bot.Send(video); err != nil {
		log.Printf("❌ Failed to send video: %v", err)
		sendDocument(bot, chatID, filePath, replyTo)
	} else {
		os.Remove(filePath)
	}
}

func sendPhoto(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int) {
	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
	photo.Caption = "@downloaderin123_bot orqali yuklab olindi"
	photo.ReplyToMessageID = replyTo
	if _, err := bot.Send(photo); err != nil {
		log.Printf("❌ Failed to send photo: %v", err)
		sendDocument(bot, chatID, filePath, replyTo)
	} else {
		os.Remove(filePath)
	}
}

func sendDocument(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int) {
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
	doc.Caption = "⚠️ Fayl hajmi katta bo‘lgani uchun hujjat sifatida yuborildi."
	doc.ReplyToMessageID = replyTo
	if _, err := bot.Send(doc); err != nil {
		log.Printf("❌ Failed to send document: %v", err)
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Faylni yuborib bo‘lmadi."))
	}
	os.Remove(filePath)
}
