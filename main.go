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
	youtubeCookiesFile = "youtube.com_cookies.txt"
	sem                = make(chan struct{}, 3)
)

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

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env file not found, using system environment")
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
		log.Fatalf("❌ Failed to create downloads folder: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Bot init failed: %v", err)
	}
	log.Printf("🤖 Bot authorized as @%s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			go handleMessage(bot, update.Message)
		}

		// 🆕 Handle button callbacks
		if update.CallbackQuery != nil {
			go handleCallback(bot, update.CallbackQuery)
		}
	}
}

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
				log.Printf("❌ Download error: %v", err)
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi."))
				return
			}

			if len(files) > 0 {
				sendVideo(bot, chatID, files[0], replyToID, url) // 🆕 pass URL for button
			}
		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

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

func downloadVideo(url string) ([]string, error) {
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))
	isYouTube := strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")

	args := []string{
		"--no-playlist", "--no-warnings", "--restrict-filenames",
		"--merge-output-format", "mp4",
		"--ffmpeg-location", ffmpegPath,
		"-o", outputTemplate,
	}

	if isYouTube && fileExists(youtubeCookiesFile) {
		args = append(args, "--cookies", youtubeCookiesFile)
	} else if !isYouTube && fileExists(instaCookiesFile) {
		args = append(args, "--cookies", instaCookiesFile)
	}

	if isYouTube {
		args = append(args, "-f", "bv*[height<=720]+ba/best[height<=720]/best")
	} else {
		args = append(args, "-f", "best")
	}

	args = append(args, url)
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output:\n%s", out)
	if err != nil {
		return nil, fmt.Errorf("yt-dlp failed: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(downloadsDir, fmt.Sprintf("%d_*.*", uniqueID)))
	if len(files) == 0 {
		return nil, fmt.Errorf("no file found")
	}
	return files, nil
}

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
func sendVideo(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int, videoURL string) {
	msg := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = "🎥 Video"
	msg.ReplyToMessageID = replyToMessageID

	// 🆕 Add inline keyboard
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💬 Eng ko‘p like yig‘gan kommentlar", "comments|"+videoURL),
			tgbotapi.NewInlineKeyboardButtonData("➕ Guruhga qo‘shish", "add_group"),
		),
	)
	msg.ReplyMarkup = keyboard

	if _, err := bot.Send(msg); err != nil {
		log.Printf("❌ Failed to send video: %v", err)
		sendDocument(bot, chatID, filePath, replyToMessageID)
	}
	os.Remove(filePath)
}

func sendDocument(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
	doc.Caption = "⚠️ Fayl hajmi katta bo‘lgani uchun hujjat sifatida yuborildi."
	doc.ReplyToMessageID = replyToMessageID
	bot.Send(doc)
	os.Remove(filePath)
}

// ===================== CALLBACK HANDLER =====================
func handleCallback(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery) {
	data := cb.Data
	chatID := cb.Message.Chat.ID

	if strings.HasPrefix(data, "comments|") {
		videoURL := strings.TrimPrefix(data, "comments|")
		bot.Send(tgbotapi.NewMessage(chatID, "⏳ Kommentlar yuklanmoqda..."))

		out, err := runCommandCapture(ytDlpPath, "--get-comments", "--max-comments", "10", "--extractor-args", "youtubetab:comment_sort=top", videoURL)
		if err != nil || strings.TrimSpace(out) == "" {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Kommentlarni yuklab bo‘lmadi."))
			return
		}
		if len(out) > 4000 {
			out = out[:4000] + "..."
		}
		bot.Send(tgbotapi.NewMessage(chatID, "💬 Eng ko‘p like olgan kommentlar:\n\n"+out))
	}

	if data == "add_group" {
		msg := "➕ Botni guruhga qo‘shish uchun bu yerdan foydalaning:\n\n👉 https://t.me/" + bot.Self.UserName + "?startgroup=true"
		bot.Send(tgbotapi.NewMessage(chatID, msg))
	}

	// Acknowledge callback
	bot.Request(tgbotapi.NewCallback(cb.ID, "✅"))
}
