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
	ffmpegPath     = "/usr/bin"
	ytDlpPath      = "yt-dlp"
	instaloaderCmd = "instaloader"
)

var (
	downloadsDir       = "downloads"
	youtubeCookiesFile = "youtube_cookies.txt"
	sem                = make(chan struct{}, 3)
)

// ===================== HEALTH CHECK =====================
func startHealthCheckServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "✅ Bot is running fine.")
	})
	log.Printf("💚 Health check server on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Health server failed: %v", err)
	}
}

// ===================== MAIN =====================
func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env file not found.")
	}

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN not set in environment.")
	}

	instaUser := os.Getenv("INSTA_USER")
	instaPass := os.Getenv("INSTA_PASS")
	if instaUser == "" || instaPass == "" {
		log.Println("⚠️ Instagram login not set (INSTA_USER / INSTA_PASS). Instaloader may fail for private posts.")
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
		startMsg := "👋 Salom!\n\n🎥 Menga YouTube, Instagram, TikTok yoki Pinterest link yuboring — men sizga media faylni yuboraman."
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

		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda... iltimos kuting.")
		sent, _ := bot.Send(loadingMsg)

		go func(url string, chatID int64, msgID, loadMsgID int) {
			sem <- struct{}{}
			files, err := downloadMedia(url)
			<-sem

			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: loadMsgID})

			if err != nil {
				log.Printf("❌ Error: %v", err)
				errMsg := tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Linkni tekshiring.")
				errMsg.ReplyToMessageID = msgID
				bot.Send(errMsg)
				return
			}

			for _, file := range files {
				sendMedia(bot, chatID, file, msgID)
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

func isSupportedLink(link string) bool {
	link = strings.ToLower(link)
	return strings.Contains(link, "youtube.com") ||
		strings.Contains(link, "youtu.be") ||
		strings.Contains(link, "instagram.com") ||
		strings.Contains(link, "tiktok.com") ||
		strings.Contains(link, "pinterest.com")
}

// ===================== DOWNLOAD HANDLER =====================
func downloadMedia(url string) ([]string, error) {
	uniqueID := time.Now().UnixNano()

	isInstagram := strings.Contains(url, "instagram.com")
	isYouTube := strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
	isTikTok := strings.Contains(url, "tiktok.com")
	isPinterest := strings.Contains(url, "pinterest.com")

	if isInstagram {
		if strings.Contains(url, "/p/") {
			return downloadWithInstaloader(url, uniqueID)
		} else if strings.Contains(url, "/reel/") || strings.Contains(url, "/stories/") {
			return downloadWithYtDlp(url, uniqueID, false)
		}
	}

	if isYouTube || isTikTok {
		return downloadWithYtDlp(url, uniqueID, isYouTube)
	}

	if isPinterest {
		return downloadWithGalleryDL(url, uniqueID)
	}

	return nil, fmt.Errorf("unsupported URL: %s", url)
}

// ===================== INSTALOADER =====================
func downloadWithInstaloader(url string, uniqueID int64) ([]string, error) {
	outputDir := filepath.Join(downloadsDir, fmt.Sprintf("%d_instagram", uniqueID))
	os.MkdirAll(outputDir, 0755)

	instaUser := os.Getenv("INSTA_USER")
	instaPass := os.Getenv("INSTA_PASS")

	args := []string{
		"--no-metadata-json",
		"--dirname-pattern", outputDir,
		"--filename-pattern", "{shortcode}",
		"--login", instaUser,
		"--password", instaPass,
		url,
	}

	log.Printf("📸 Downloading Instagram photo via Instaloader: %s", url)
	out, err := runCommandCapture(instaloaderCmd, args...)
	log.Printf("📄 Instaloader output:\n%s", out)

	if err != nil {
		return nil, fmt.Errorf("instaloader failed: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(outputDir, "*"))
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found after instaloader")
	}
	return files, nil
}

// ===================== YT-DLP =====================
func downloadWithYtDlp(url string, uniqueID int64, isYouTube bool) ([]string, error) {
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))
	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--restrict-filenames",
		"--merge-output-format", "mp4",
		"--ffmpeg-location", ffmpegPath,
		"--no-check-certificates",
		"-o", outputTemplate,
	}

	if isYouTube && fileExists(youtubeCookiesFile) {
		args = append(args, "--cookies", youtubeCookiesFile)
	}

	args = append(args, url)

	log.Printf("🎬 Downloading with yt-dlp: %s", url)
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("📄 yt-dlp output:\n%s", out)

	if err != nil {
		return nil, fmt.Errorf("yt-dlp failed: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(downloadsDir, fmt.Sprintf("%d_*.*", uniqueID)))
	if len(files) == 0 {
		return nil, fmt.Errorf("no file found after yt-dlp download")
	}
	return files, nil
}

// ===================== GALLERY-DL =====================
func downloadWithGalleryDL(url string, uniqueID int64) ([]string, error) {
	outputDir := filepath.Join(downloadsDir, fmt.Sprintf("%d_pinterest", uniqueID))
	os.MkdirAll(outputDir, 0755)

	args := []string{"-d", outputDir, url}

	log.Printf("📌 Downloading Pinterest content via gallery-dl: %s", url)
	out, err := runCommandCapture("gallery-dl", args...)
	log.Printf("📄 gallery-dl output:\n%s", out)

	if err != nil {
		return nil, fmt.Errorf("gallery-dl failed: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(outputDir, "*"))
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found after gallery-dl")
	}
	return files, nil
}

// ===================== HELPERS =====================
func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ===================== SENDERS =====================
func sendMedia(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToID int) {
	if strings.HasSuffix(strings.ToLower(filePath), ".jpg") || strings.HasSuffix(strings.ToLower(filePath), ".png") {
		sendPhoto(bot, chatID, filePath, replyToID)
	} else {
		sendVideo(bot, chatID, filePath, replyToID)
	}
}

func sendPhoto(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToID int) {
	msg := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = "@downloaderin123_bot orqali yuklab olindi"
	msg.ReplyToMessageID = replyToID
	msg.ReplyMarkup = groupButton()

	if _, err := bot.Send(msg); err != nil {
		log.Printf("❌ Failed to send image: %v", err)
	} else {
		os.Remove(filePath)
	}
}

func sendVideo(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToID int) {
	msg := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = "@downloaderin123_bot orqali yuklab olindi"
	msg.ReplyToMessageID = replyToID
	msg.ReplyMarkup = groupButton()

	if _, err := bot.Send(msg); err != nil {
		log.Printf("❌ Failed to send video: %v", err)
		sendDocument(bot, chatID, filePath, replyToID)
	} else {
		os.Remove(filePath)
	}
}

func sendDocument(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToID int) {
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
	doc.Caption = "@downloaderin123_bot orqali yuklab olindi"
	doc.ReplyToMessageID = replyToID
	doc.ReplyMarkup = groupButton()

	if _, err := bot.Send(doc); err != nil {
		log.Printf("❌ Failed to send document: %v", err)
	} else {
		os.Remove(filePath)
	}
}

func groupButton() tgbotapi.InlineKeyboardMarkup {
	button := tgbotapi.NewInlineKeyboardButtonURL("➕ Guruhga qo‘shish", "https://t.me/downloaderin123_bot?startgroup=true")
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(button),
	)
}
