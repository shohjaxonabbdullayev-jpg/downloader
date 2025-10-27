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

const ytDlpPath = "yt-dlp" // Dockerfile yoki muhitda yt-dlp shu nom bilan mavjud bo'lishi kerak

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 3) // bir vaqtda maksimal 3 yuklab olish
)

func main() {
	// Lokal uchun .env yuklash (Renderda kerak emas)
	_ = godotenv.Load()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN muhit o'zgaruvchisi o'rnatilmagan")
	}

	renderURL := os.Getenv("RENDER_EXTERNAL_URL")
	if renderURL == "" {
		log.Fatal("❌ RENDER_EXTERNAL_URL muhit o'zgaruvchisi o'rnatilmagan (Render avtomatik beradi)")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "10000"
	}

	// downloads papkasini tayyorlash
	os.RemoveAll(downloadsDir)
	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		log.Fatalf("❌ downloads papkasini yaratib bo'lmadi: %v", err)
	}

	// Telegram bot
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Telegram botni ishga tushirib bo'lmadi: %v", err)
	}
	log.Printf("🤖 Bot ishga tushdi: @%s", bot.Self.UserName)

	// Webhook sozlash
	webhookURL := fmt.Sprintf("%s/%s", renderURL, bot.Token)
	wh, err := tgbotapi.NewWebhook(webhookURL)
	if err != nil {
		log.Fatalf("❌ Webhook konfiguratsiyasi yaratib bo'lmadi: %v", err)
	}
	if _, err := bot.Request(wh); err != nil {
		log.Fatalf("❌ Webhookni o'rnatib bo'lmadi: %v", err)
	}

	if info, err := bot.GetWebhookInfo(); err == nil {
		log.Printf("🌍 Webhook ulandi: %s", info.URL)
	}

	// Health endpoint
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	// Telegram webhook listener
	updates := bot.ListenForWebhook("/" + bot.Token)

	// Start HTTP server
	go func() {
		log.Printf("🌐 Server %s-portda ishlamoqda...", port)
		log.Fatal(http.ListenAndServe(":"+port, nil))
	}()

	// Keep-alive ping (Render uyquga ketmasligi uchun)
	go keepAlive(renderURL)

	// Updatelarni qayta ishlash
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
		startMsg := "👋 Assalomu alaykum!\n\n🎥 Menga YouTube yoki Instagram video link yuboring — men uni yuklab beraman."
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Faqat YouTube yoki Instagram link yuboring."))
		return
	}

	for _, link := range links {
		if strings.Contains(link, "youtube.com/shorts/") {
			link = strings.Replace(link, "shorts/", "watch?v=", 1)
		}

		loading := tgbotapi.NewMessage(chatID, "⏳ Video yuklanmoqda, biroz kuting...")
		loading.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(loading)

		go func(url string, chatID int64, replyToID, loadingMsgID int) {
			sem <- struct{}{}
			files, err := downloadVideo(url)
			<-sem

			// loading xabarini o'chirish
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Yuklab olishda xato (%s): %v", url, err)
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Videoni yuklab bo‘lmadi."))
				return
			}

			// video yuborish
			sendVideo(bot, chatID, files[0], replyToID)

			// tozalash
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
		l := strings.TrimSpace(m)
		if isSupportedLink(l) {
			links = append(links, l)
		}
	}
	return links
}

func isSupportedLink(text string) bool {
	text = strings.ToLower(text)
	return strings.Contains(text, "youtube.com") ||
		strings.Contains(text, "youtu.be") ||
		strings.Contains(text, "instagram.com") ||
		strings.Contains(text, "instagr.am")
}

// ===================== DOWNLOAD VIDEO (with cookies.txt support) =====================
func downloadVideo(url string) ([]string, error) {
	id := time.Now().UnixNano()
	output := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", id))

	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--merge-output-format", "mp4",
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
		"--add-header", "Referer:https://www.instagram.com/",
		"--no-check-certificates",
		"-o", output,
	}

	// cookies.txt qo'llash
	if _, err := os.Stat("cookies.txt"); err == nil {
		args = append(args, "--cookies", "cookies.txt")
		log.Println("🍪 cookies.txt topildi — avtorizatsiyalangan yuklab olish yoqildi")
	}

	// format tanlash
	if strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be") {
		args = append(args, "-f", "bestvideo[height<=720]+bestaudio/best[height<=720]")
	} else {
		args = append(args, "-f", "bestvideo+bestaudio/best")
	}

	args = append(args, url)

	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp chiqishi (%s):\n%s", url, out)
	if err != nil {
		return nil, fmt.Errorf("yt-dlp ishlamay qoldi: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(downloadsDir, fmt.Sprintf("%d_*.*", id)))
	if len(files) == 0 {
		time.Sleep(2 * time.Second)
		files, _ = filepath.Glob(filepath.Join(downloadsDir, fmt.Sprintf("%d_*.*", id)))
		if len(files) == 0 {
			return nil, fmt.Errorf("⚠️ Yuklab olindi, lekin fayl topilmadi")
		}
	}

	return files, nil
}

// ===================== RUN COMMAND =====================
func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// ===================== SEND VIDEO =====================
func sendVideo(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int) {
	msg := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = "🎬 Video tayyor!"
	msg.ReplyToMessageID = replyTo
	_, _ = bot.Send(msg)
}

// ===================== KEEP ALIVE =====================
func keepAlive(renderURL string) {
	if renderURL == "" {
		log.Println("⚠️ keepAlive o'tkazildi (RENDER_EXTERNAL_URL yo'q)")
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
