package main

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	"golang.org/x/image/webp"
)

// ⚙️ Constants
const (
	commandTimeout = 3 * time.Minute
	defaultPort    = "10000"
)

// ✅ Automatically add ffmpeg to PATH (adjust if needed)
func init() {
	ffmpegPath := `C:\Users\user\Desktop\ffmpeg-8.0-full_build\bin`
	os.Setenv("PATH", os.Getenv("PATH")+";"+ffmpegPath)
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("⚠️ No .env file found, continuing...")
	}

	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		log.Fatal("❌ BOT_TOKEN not set in environment")
	}

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("❌ Failed to create bot: %v", err)
	}

	log.Printf("✅ Bot authorized as @%s", bot.Self.UserName)

	// 🟢 Start ping server for Render
	startPingServer()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		text := update.Message.Text
		if strings.HasPrefix(text, "http") {
			go handleDownload(bot, update.Message.Chat.ID, update.Message.MessageID, text)
		}
	}
}

// 🌐 Health check server (keeps Render alive)
func startPingServer() {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "✅ Bot is running")
	})

	go func() {
		log.Printf("🌐 Health server running on port %s", port)
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			log.Fatalf("Ping server error: %v", err)
		}
	}()
}

// 🎬 Main download handler
func handleDownload(bot *tgbotapi.BotAPI, chatID int64, replyTo int, url string) {
	// Send "yuklanmoqda..." message
	waitMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda...")
	waitMsg.ReplyToMessageID = replyTo
	sent, _ := bot.Send(waitMsg)

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	os.MkdirAll("downloads", 0755)
	outputFile := filepath.Join("downloads", "output.%(ext)s")

	cmd := exec.CommandContext(ctx, "yt-dlp", "-f", "best", "-o", outputFile, url)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		log.Printf("❌ Download error: %v | %s", err, stderr.String())
		sendError(bot, chatID, replyTo)
		deleteMsg(bot, chatID, sent.MessageID)
		return
	}

	files, _ := filepath.Glob("downloads/output.*")
	if len(files) == 0 {
		sendError(bot, chatID, replyTo)
		deleteMsg(bot, chatID, sent.MessageID)
		return
	}
	filePath := files[0]

	// Detect and send correct media type
	if strings.HasSuffix(filePath, ".mp4") || strings.HasSuffix(filePath, ".mov") {
		sendVideo(bot, chatID, replyTo, filePath)
	} else if strings.HasSuffix(filePath, ".jpg") || strings.HasSuffix(filePath, ".png") || strings.HasSuffix(filePath, ".webp") {
		sendImage(bot, chatID, replyTo, filePath)
	} else {
		sendAudio(bot, chatID, replyTo, filePath)
	}

	os.Remove(filePath)
	deleteMsg(bot, chatID, sent.MessageID)
}

// 🎥 Send video
func sendVideo(bot *tgbotapi.BotAPI, chatID int64, replyTo int, filePath string) {
	video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	video.ReplyToMessageID = replyTo
	_, err := bot.Send(video)
	if err != nil {
		log.Printf("❌ Video send error: %v", err)
		sendError(bot, chatID, replyTo)
	}
}

// 🎧 Send audio
func sendAudio(bot *tgbotapi.BotAPI, chatID int64, replyTo int, filePath string) {
	audioPath := strings.TrimSuffix(filePath, filepath.Ext(filePath)) + ".mp3"

	cmd := exec.Command("ffmpeg", "-i", filePath, "-vn", "-acodec", "libmp3lame", "-ab", "192k", audioPath)
	err := cmd.Run()
	if err != nil {
		log.Printf("❌ FFmpeg conversion error: %v", err)
		sendError(bot, chatID, replyTo)
		return
	}

	audio := tgbotapi.NewAudio(chatID, tgbotapi.FilePath(audioPath))
	audio.ReplyToMessageID = replyTo
	_, err = bot.Send(audio)
	if err != nil {
		log.Printf("❌ Audio send error: %v", err)
		sendError(bot, chatID, replyTo)
	}

	os.Remove(audioPath)
}

// 🖼️ Send image
func sendImage(bot *tgbotapi.BotAPI, chatID int64, replyTo int, filePath string) {
	if strings.HasSuffix(filePath, ".webp") {
		img, err := os.Open(filePath)
		if err != nil {
			sendError(bot, chatID, replyTo)
			return
		}
		defer img.Close()

		webpImg, err := webp.Decode(img)
		if err != nil {
			sendError(bot, chatID, replyTo)
			return
		}

		jpgPath := strings.TrimSuffix(filePath, ".webp") + ".jpg"
		out, err := os.Create(jpgPath)
		if err != nil {
			sendError(bot, chatID, replyTo)
			return
		}
		defer out.Close()

		if err := jpeg.Encode(out, webpImg, nil); err != nil {
			sendError(bot, chatID, replyTo)
			return
		}
		filePath = jpgPath
	}

	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
	photo.ReplyToMessageID = replyTo
	_, err := bot.Send(photo)
	if err != nil {
		log.Printf("❌ Photo send error: %v", err)
		sendError(bot, chatID, replyTo)
	}
}

// 🗑️ Delete “Yuklanmoqda...” message
func deleteMsg(bot *tgbotapi.BotAPI, chatID int64, messageID int) {
	del := tgbotapi.NewDeleteMessage(chatID, messageID)
	bot.Request(del)
}

// ⚠️ Send error message
func sendError(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	msg := tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Qayta urinib ko‘ring.")
	msg.ReplyToMessageID = replyTo
	bot.Send(msg)
}
