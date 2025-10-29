package main

import (
	"bytes"
	"encoding/json"
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
		if update.Message != nil {
			go handleMessage(bot, update.Message)
		} else if update.CallbackQuery != nil {
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
		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda... iltimos kuting.")
		sent, _ := bot.Send(loadingMsg)

		go func(url string, chatID int64, msgID, loadingMsgID int) {
			sem <- struct{}{}
			files, err := downloadVideo(url)
			<-sem

			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", url, err)
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Linkni tekshiring."))
				return
			}

			if len(files) == 0 {
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Fayl topilmadi."))
				return
			}

			// Send video
			sendVideo(bot, chatID, files[0], msgID)

			// Inline buttons
			keyboard := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("🗨️ 10 eng qiziqarli commentlar", "comments|"+url),
				),
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonURL("🎵 Guruhga qo'shish", "https://t.me/+something"),
				),
			)

			msg := tgbotapi.NewMessage(chatID, "Tanlang:")
			msg.ReplyMarkup = keyboard
			bot.Send(msg)

		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

func handleCallback(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery) {
	data := query.Data
	if strings.HasPrefix(data, "comments|") {
		url := strings.TrimPrefix(data, "comments|")
		bot.Send(tgbotapi.NewMessage(query.Message.Chat.ID, "💬 Eng qiziqarli 10 ta comment yuklanmoqda..."))

		go func() {
			comments, err := fetchTopComments(url)
			if err != nil {
				bot.Send(tgbotapi.NewMessage(query.Message.Chat.ID, "❌ Commentlarni olishda xatolik yuz berdi."))
				return
			}

			if len(comments) == 0 {
				bot.Send(tgbotapi.NewMessage(query.Message.Chat.ID, "😕 Commentlar topilmadi."))
				return
			}

			result := "💬 *10 ta eng qiziqarli commentlar:*\n\n"
			for i, c := range comments {
				result += fmt.Sprintf("%d️⃣ %s\n\n", i+1, c)
			}

			msg := tgbotapi.NewMessage(query.Message.Chat.ID, result)
			msg.ParseMode = "Markdown"
			bot.Send(msg)
		}()
	}
}

// =============== Fetch top 10 comments using yt-dlp ===============
func fetchTopComments(url string) ([]string, error) {
	args := []string{
		"--skip-download",
		"--extractor-args", "instagram:comments",
		"--print", "%(comments)s",
		url,
	}

	out, err := runCommandCapture(ytDlpPath, args...)
	if err != nil {
		return nil, fmt.Errorf("yt-dlp error: %v", err)
	}

	type Comment struct {
		Text  string `json:"text"`
		Likes int    `json:"like_count"`
	}

	var allComments []Comment
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}

	// yt-dlp prints list of JSON objects — clean it
	out = strings.Trim(out, "[]")
	out = strings.ReplaceAll(out, "}, {", "}|{")
	chunks := strings.Split(out, "|")
	for _, c := range chunks {
		var comment Comment
		json.Unmarshal([]byte(strings.TrimSpace(c)), &comment)
		if comment.Text != "" {
			allComments = append(allComments, comment)
		}
	}

	// Sort by likes
	for i := 0; i < len(allComments)-1; i++ {
		for j := i + 1; j < len(allComments); j++ {
			if allComments[j].Likes > allComments[i].Likes {
				allComments[i], allComments[j] = allComments[j], allComments[i]
			}
		}
	}

	// Take top 10
	limit := 10
	if len(allComments) < 10 {
		limit = len(allComments)
	}

	var result []string
	for i := 0; i < limit; i++ {
		result = append(result, allComments[i].Text)
	}

	return result, nil
}

// =============== Existing helpers below ===============

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

	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--restrict-filenames",
		"--merge-output-format", "mp4",
		"--ffmpeg-location", ffmpegPath,
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
		"--no-check-certificates",
		"-o", outputTemplate,
		"-f", "best",
		url,
	}

	log.Printf("⚙️ Downloading with yt-dlp: %s", url)
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output:\n%s", out)

	if err != nil {
		return nil, fmt.Errorf("yt-dlp failed: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(downloadsDir, fmt.Sprintf("%d_*.*", uniqueID)))
	if len(files) == 0 {
		return nil, fmt.Errorf("no file found after download")
	}

	return files, nil
}

func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

func sendVideo(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	msg := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	msg.Caption = "🎥 Video"
	msg.ReplyToMessageID = replyToMessageID

	if _, err := bot.Send(msg); err != nil {
		log.Printf("❌ Failed to send video: %v", err)
	} else {
		os.Remove(filePath)
	}
}
