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
	ffmpegPath = "/usr/bin" // Path for Render/Docker Linux environment
	ytDlpPath  = "yt-dlp"   // yt-dlp is installed via pip and should be in PATH
)

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 3) // Limit concurrent downloads to 3
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
		log.Println("⚠️ .env file not found, using system environment (Render uses env vars)")
	}

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN not set in environment or .env file")
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
		startMsg := "👋 Salom!\n\n🎥 Menga YouTube, Instagram yoki TikTok link yuboring — men sizga videoni yuboraman."
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
		loadingMsg.ReplyToMessageID = msg.MessageID
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
				log.Printf("❌ Download error for %s: %v", url, err)
				errorMsg := tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Iltimos, linkning to‘g‘ri ekanligiga ishonch hosil qiling.")
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

			if len(files) > 0 {
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
func downloadVideo(url string) ([]string, error) {
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))
	isYouTube := strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")

	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--restrict-filenames",
		"--merge-output-format", "mp4",
		"--ffmpeg-location", ffmpegPath,
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
		"--add-header", "Referer:https://www.youtube.com/",
		"--add-header", "Accept-Language:en-US,en;q=0.9",
		"--no-check-certificates",
		"-o", outputTemplate,
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

	if err != nil {
		if isYouTube {
			log.Println("🔁 Retrying YouTube download with simpler format...")
			args = []string{"-f", "best", "-o", outputTemplate, url}
			out, err = runCommandCapture(ytDlpPath, args...)
			log.Printf("🧾 Retry output:\n%s", out)
		}
		if err != nil {
			return nil, fmt.Errorf("yt-dlp failed: %v", err)
		}
	}

	files, _ := filepath.Glob(filepath.Join(downloadsDir, fmt.Sprintf("%d_*.*", uniqueID)))
	if len(files) == 0 {
		time.Sleep(1 * time.Second)
		files, _ = filepath.Glob(filepath.Join(downloadsDir, fmt.Sprintf("%d_*.*", uniqueID)))
		if len(files) == 0 {
			return nil, fmt.Errorf("no file found after download")
		}
	}

	log.Printf("✅ Download complete: %s", files[0])
	return files, nil
}

// ===================== COMMAND RUNNER =====================
func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

// ===================== SENDERS =====================
func sendVideo(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	msg := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = "🎥 Video"
	msg.ReplyToMessageID = replyToMessageID

	if _, err := bot.Send(msg); err != nil {
		log.Printf("❌ Failed to send video %s: %v", filePath, err)
		sendDocument(bot, chatID, filePath, replyToMessageID)
	} else {
		if err := os.Remove(filePath); err != nil {
			log.Printf("⚠️ Failed to delete file after sending video %s: %v", filePath, err)
		} else {
			log.Printf("🧹 Deleted file after sending video: %s", filePath)
		}
	}
}

func sendDocument(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
	doc.Caption = "⚠️ Fayl hajmi katta bo‘lgani uchun hujjat sifatida yuborildi."
	doc.ReplyToMessageID = replyToMessageID

	if _, err := bot.Send(doc); err != nil {
		log.Printf("❌ Failed to send document %s: %v", filePath, err)
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Faylni Telegramga yuklab bo‘lmadi."))
	} else {
		if err := os.Remove(filePath); err != nil {
			log.Printf("⚠️ Failed to delete file after sending document %s: %v", filePath, err)
		} else {
			log.Printf("🧹 Deleted file after sending document: %s", filePath)
		}
	}
}
