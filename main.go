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
	ffmpegPath   = "" // ffmpeg Render'da tizimda o‘rnatilgan
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 3) // bir vaqtda maksimal 3 ta yuklab olish
)

func main() {
	// .env yuklash (faqat lokal ishda)
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env fayl topilmadi — tizimdagi o‘zgaruvchilar ishlatiladi")
	}

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN muhit o‘zgaruvchisi o‘rnatilmagan")
	}

	// Yuklab olish papkasini tayyorlash
	os.RemoveAll(downloadsDir)
	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		log.Fatalf("❌ downloads papkasi yaratilolmadi: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Telegram bot ishga tushmadi: %v", err)
	}

	log.Printf("🤖 Bot ishga tushdi: @%s", bot.Self.UserName)

	// Fon jarayonlar
	go startHealthServer()
	go keepAlive()

	// Telegram xabarlarini olish
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

// ===================== XABARLARNI QABUL QILISH =====================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	chatID := msg.Chat.ID

	if text == "/start" {
		startMsg := "👋 Assalomu alaykum!\n\n🎥 Menga *YouTube* yoki *Instagram* videoning linkini yuboring, men esa sizga videoni yuboraman.\n\nMasalan:\n`https://www.youtube.com/watch?v=abc123`\n`https://www.instagram.com/reel/xyz456/`"
		m := tgbotapi.NewMessage(chatID, startMsg)
		m.ParseMode = "Markdown"
		bot.Send(m)
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		// faqat YouTube va Instagram linklarini qabul qiladi
		return
	}

	for _, link := range links {
		if strings.Contains(link, "youtube.com/shorts/") {
			link = strings.Replace(link, "shorts/", "watch?v=", 1)
		}

		waitMsg := tgbotapi.NewMessage(chatID, "⏳ Video yuklanmoqda, biroz kuting...")
		waitMsg.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(waitMsg)

		go func(url string, chatID int64, replyToID, waitMsgID int) {
			sem <- struct{}{}
			files, err := downloadVideo(url)
			<-sem

			// "yuklanmoqda..." xabarini o‘chirish
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: waitMsgID,
			})

			if err != nil {
				log.Printf("❌ Yuklab olishda xato (%s): %v", url, err)
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Videoni yuklab bo‘lmadi. Ehtimol link xato yoki maxfiy hisob."))
				return
			}

			sendVideo(bot, chatID, files[0], replyToID)

			for _, f := range files {
				os.Remove(f)
			}
		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

// ===================== LINKNI AJRATISH =====================
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
		strings.Contains(text, "instagr.am")
}

// ===================== VIDEO YUKLASH =====================
func downloadVideo(url string) ([]string, error) {
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	isYouTube := strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")

	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--merge-output-format", "mp4",
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
		"--add-header", "Referer:https://www.instagram.com/",
		"--add-header", "Accept-Language:uz-UZ,uz;q=0.9,en;q=0.8",
		"--no-check-certificates",
		"-o", outputTemplate,
	}

	// Agar cookies.txt mavjud bo‘lsa, qo‘shamiz
	if _, err := os.Stat("cookies.txt"); err == nil {
		args = append(args, "--cookies", "cookies.txt")
	}

	if ffmpegPath != "" {
		args = append(args, "--ffmpeg-location", ffmpegPath)
	}

	if isYouTube {
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

	files, _ := filepath.Glob(filepath.Join(downloadsDir, fmt.Sprintf("%d_*.*", uniqueID)))
	if len(files) == 0 {
		log.Println("⚠️ Fayl topilmadi, qayta tekshirilmoqda...")
		time.Sleep(2 * time.Second)
		files, _ = filepath.Glob(filepath.Join(downloadsDir, fmt.Sprintf("%d_*.*", uniqueID)))
		if len(files) == 0 {
			return nil, fmt.Errorf("⚠️ Yuklab olindi, lekin fayl topilmadi")
		}
	}

	return files, nil
}

// ===================== BUYRUQ ISHLATISH =====================
func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

// ===================== VIDEO YUBORISH =====================
func sendVideo(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	msg := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = "🎥 Video tayyor!"
	msg.ReplyToMessageID = replyToMessageID
	bot.Send(msg)
}

// ===================== HEALTH TEKSHIRUV =====================
func startHealthServer() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000" // lokal test uchun
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	log.Printf("🌐 Health check server %s-portda ishga tushdi", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// ===================== RENDER UCHUN UYQON TURI =====================
func keepAlive() {
	renderURL := os.Getenv("RENDER_EXTERNAL_URL")
	if renderURL == "" {
		log.Println("⚠️ RENDER_EXTERNAL_URL belgilanmagan — ping yuborilmaydi")
		return
	}

	for {
		time.Sleep(5 * time.Minute)
		url := fmt.Sprintf("https://%s/healthz", renderURL)
		resp, err := http.Get(url)
		if err != nil {
			log.Printf("⚠️ Ping yuborishda xato: %v", err)
			continue
		}
		resp.Body.Close()
		log.Printf("💓 Render uyg‘oq holatda (%s)", url)
	}
}
