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

const ytDlpPath = "yt-dlp"

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 3) // parallel download limiter
)

func main() {
	// Load .env locally
	_ = godotenv.Load()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN o‘rnatilmagan (.env yoki Render environment)")
	}

	renderURL := os.Getenv("RENDER_EXTERNAL_URL")
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000"
	}

	// Create downloads directory
	os.RemoveAll(downloadsDir)
	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		log.Fatalf("❌ Yuklab olish papkasi yaratilmadi: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Botni ishga tushirib bo‘lmadi: %v", err)
	}
	log.Printf("🤖 Bot ishga tushdi: @%s", bot.Self.UserName)

	// ---------------- SETUP WEBHOOK ----------------
	if renderURL == "" {
		log.Fatal("❌ RENDER_EXTERNAL_URL o‘rnatilmagan — webhook uchun zarur")
	}

	webhookURL := fmt.Sprintf("%s/%s", renderURL, bot.Token)
	wh, err := tgbotapi.NewWebhook(webhookURL)
	if err != nil {
		log.Fatalf("❌ Webhook konfiguratsiyasi yaratilmadi: %v", err)
	}

	_, err = bot.Request(wh)
	if err != nil {
		log.Fatalf("❌ Webhook o‘rnatilmadi: %v", err)
	}

	info, err := bot.GetWebhookInfo()
	if err == nil {
		log.Printf("🌐 Webhook ulandi: %s", info.URL)
	}

	// Start health check
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	// Start Telegram webhook listener
	updates := bot.ListenForWebhook("/" + bot.Token)

	go func() {
		log.Printf("🌍 Server %s-portda ishlamoqda...", port)
		log.Fatal(http.ListenAndServe(":"+port, nil))
	}()

	// Start keep-alive ping (Render uyquga ketmasligi uchun)
	go keepAlive(renderURL)

	for update := range updates {
		if update.Message == nil {
			continue
		}
		go handleMessage(bot, update.Message)
	}
}

// ===================== HANDLE MESSAGES =====================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	chatID := msg.Chat.ID

	if text == "/start" {
		startMsg := "👋 Salom!\n\n🎥 Menga YouTube yoki Instagram link yuboring — men sizga videoni yuklab beraman."
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

		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Video yuklanmoqda, biroz kuting...")
		sent, _ := bot.Send(loadingMsg)

		go func(url string, chatID int64, replyToID, loadingMsgID int) {
			sem <- struct{}{}
			files, err := downloadVideo(url)
			<-sem

			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Yuklab olishda xato (%s): %v", url, err)
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Videoni yuklab bo‘lmadi."))
				return
			}

			sendVideo(bot, chatID, files[0], replyToID)

			for _, f := range files {
				os.Remove(f)
			}
		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

// ===================== LINK DETECTION =====================
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
		strings.Contains(text, "instagram.com")
}

// ===================== DOWNLOAD VIDEO =====================
func downloadVideo(url string) ([]string, error) {
	id := time.Now().UnixNano()
	output := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", id))

	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--merge-output-format", "mp4",
		"--user-agent", "Mozilla/5.0",
		"--add-header", "Referer:https://www.instagram.com/",
		"--no-check-certificates",
		"-o", output,
		"-f", "bestvideo[height<=720]+bestaudio/best",
		url,
	}

	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp chiqishi (%s):\n%s", url, out)

	if err != nil {
		return nil, fmt.Errorf("yt-dlp ishlamay qoldi: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(downloadsDir, fmt.Sprintf("%d_*.*", id)))
	if len(files) == 0 {
		return nil, fmt.Errorf("⚠️ Fayl topilmadi")
	}
	return files, nil
}

// ===================== UTIL FUNCTIONS =====================
func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func sendVideo(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int) {
	msg := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = "🎬 Video tayyor!"
	msg.ReplyToMessageID = replyTo
	bot.Send(msg)
}

// ===================== KEEP ALIVE =====================
func keepAlive(renderURL string) {
	if renderURL == "" {
		log.Println("⚠️ keepAlive o‘tkazildi (RENDER_EXTERNAL_URL yo‘q)")
		return
	}

	for {
		time.Sleep(5 * time.Minute)
		url := fmt.Sprintf("%s/healthz", renderURL)
		resp, err := http.Get(url)
		if err != nil {
			log.Printf("⚠️ Ping xato: %v", err)
			continue
		}
		resp.Body.Close()
		log.Printf("💓 Ping muvaffaqiyatli (%s)", url)
	}
}
