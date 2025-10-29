package main

import (
	"bytes"
	"fmt"
	"log"
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
	ytDlpPath  = "/usr/local/bin/yt-dlp"
)

var (
	instaCookiesFile   = "cookies.txt"
	youtubeCookiesFile = "youtube_cookies.txt"
)

func main() {
	// Load environment
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ No .env file found, continuing...")
	}

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN not found in .env")
	}

	// Initialize bot
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}
	bot.Debug = false
	log.Printf("🤖 Bot authorized as @%s", bot.Self.UserName)

	// Updates
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	// Ensure downloads folder exists
	os.MkdirAll("downloads", 0755)

	for update := range updates {
		if update.Message != nil {
			go handleMessage(bot, update.Message)
		} else if update.CallbackQuery != nil {
			go handleCallback(bot, update.CallbackQuery)
		}
	}
}

// ================= MESSAGE HANDLER =================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	if msg.Text == "" {
		return
	}

	url := strings.TrimSpace(msg.Text)
	if !isSupportedURL(url) {
		reply := tgbotapi.NewMessage(msg.Chat.ID, "❌ Please send a valid YouTube, Instagram, or TikTok link.")
		bot.Send(reply)
		return
	}

	sent, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⬇️ Downloading your video... Please wait."))

	filePath, err := downloadVideo(url)
	if err != nil {
		edit := tgbotapi.NewEditMessageText(msg.Chat.ID, sent.MessageID, fmt.Sprintf("❌ Download failed: %v", err))
		bot.Send(edit)
		return
	}
	defer os.Remove(filePath)

	video := tgbotapi.NewVideo(msg.Chat.ID, tgbotapi.FilePath(filePath))
	video.Caption = "✅ Download complete!"
	_, err = bot.Send(video)
	if err != nil {
		edit := tgbotapi.NewEditMessageText(msg.Chat.ID, sent.MessageID, fmt.Sprintf("❌ Error sending video: %v", err))
		bot.Send(edit)
		return
	}

	// Buttons
	var kb tgbotapi.InlineKeyboardMarkup
	if strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be") {
		kb = tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("📜 Top 10 Most Liked Comments", "comments|"+url),
				tgbotapi.NewInlineKeyboardButtonURL("➕ Add Bot to Group", "https://t.me/"+bot.Self.UserName+"?startgroup=true"),
			),
		)
	} else {
		kb = tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonURL("➕ Add Bot to Group", "https://t.me/"+bot.Self.UserName+"?startgroup=true"),
			),
		)
	}

	editMarkup := tgbotapi.NewEditMessageReplyMarkup(msg.Chat.ID, sent.MessageID, kb)
	bot.Send(editMarkup)
}

// ================= CALLBACK HANDLER =================
func handleCallback(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery) {
	data := cb.Data
	if strings.HasPrefix(data, "comments|") {
		url := strings.TrimPrefix(data, "comments|")

		msg := tgbotapi.NewMessage(cb.Message.Chat.ID, "💬 Fetching top 10 most liked comments...")
		sent, _ := bot.Send(msg)

		comments, err := fetchTopComments(url)
		if err != nil {
			edit := tgbotapi.NewEditMessageText(cb.Message.Chat.ID, sent.MessageID,
				fmt.Sprintf("❌ Failed to fetch comments: %v", err))
			bot.Send(edit)
			return
		}

		result := "📝 *Top 10 Most Liked Comments:*\n\n" + comments
		edit := tgbotapi.NewEditMessageText(cb.Message.Chat.ID, sent.MessageID, result)
		edit.ParseMode = "Markdown"
		bot.Send(edit)
	}
}

// ================= UTILITIES =================
func isSupportedURL(url string) bool {
	matched, _ := regexp.MatchString(`(youtube\.com|youtu\.be|instagram\.com|tiktok\.com)`, url)
	return matched
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ================= DOWNLOAD FUNCTION =================
func downloadVideo(url string) (string, error) {
	timestamp := time.Now().Unix()
	outPath := filepath.Join("downloads", fmt.Sprintf("video_%d.%%(ext)s", timestamp))

	args := []string{"-f", "mp4", "-o", outPath, "--no-warnings", "--no-check-certificates"}

	// Apply cookies
	if strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be") {
		if fileExists(youtubeCookiesFile) {
			args = append(args, "--cookies", youtubeCookiesFile)
			log.Println("🍪 Using YouTube cookies.")
		}
	} else if strings.Contains(url, "instagram.com") || strings.Contains(url, "tiktok.com") {
		if fileExists(instaCookiesFile) {
			args = append(args, "--cookies", instaCookiesFile)
			log.Println("🍪 Using Instagram/TikTok cookies.")
		}
	}

	args = append(args, url)
	cmd := exec.Command(ytDlpPath, args...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("yt-dlp error: %s", stderr.String())
	}

	files, _ := filepath.Glob(filepath.Join("downloads", fmt.Sprintf("video_%d.*", timestamp)))
	if len(files) == 0 {
		return "", fmt.Errorf("downloaded file not found")
	}

	return files[0], nil
}

// ================= COMMENTS FETCHER =================
func fetchTopComments(url string) (string, error) {
	args := []string{
		"--get-comments", "--skip-download", "--no-warnings",
	}
	if fileExists(youtubeCookiesFile) {
		args = append(args, "--cookies", youtubeCookiesFile)
	}
	args = append(args, url)

	cmd := exec.Command(ytDlpPath, args...)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("yt-dlp error: %v", err)
	}

	lines := strings.Split(out.String(), "\n")
	if len(lines) == 0 {
		return "", fmt.Errorf("no comments found")
	}

	if len(lines) > 10 {
		lines = lines[:10]
	}

	return strings.Join(lines, "\n"), nil
}
