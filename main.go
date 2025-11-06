package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const downloadsDir = "downloads"

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env not found, using system environment")
	}

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN not set")
	}

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
		if update.Message != nil && update.Message.Text != "" {
			go handleMessage(bot, update.Message)
		}
	}
}

func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if !strings.Contains(text, "youtube.com") && !strings.Contains(text, "youtu.be") {
		return
	}

	loadingMsg := tgbotapi.NewMessage(msg.Chat.ID, "⏳ Yuklanmoqda... iltimos kuting.")
	loadingMsg.ReplyToMessageID = msg.MessageID
	sentMsg, _ := bot.Send(loadingMsg)

	files, mediaType, err := downloadYouTube(text)
	// delete loading message
	bot.Request(tgbotapi.DeleteMessageConfig{
		ChatID:    msg.Chat.ID,
		MessageID: sentMsg.MessageID,
	})

	if err != nil {
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("⚠️ Yuklab bo‘lmadi: %v", err)))
		return
	}

	for _, file := range files {
		sendMedia(bot, msg.Chat.ID, file, msg.MessageID, mediaType)
	}
}

// ===================== YOUTUBE DOWNLOAD USING RAPIDAPI =====================
type YouTubeResponse struct {
	Success bool   `json:"success"`
	Title   string `json:"title"`
	Url     string `json:"url"` // direct download URL
}

func downloadYouTube(link string) ([]string, string, error) {
	apiKey := os.Getenv("RAPIDAPI_KEY")
	if apiKey == "" {
		return nil, "", fmt.Errorf("RAPIDAPI_KEY not set")
	}

	videoID := extractYouTubeID(link)
	if videoID == "" {
		return nil, "", fmt.Errorf("invalid YouTube link")
	}

	req, err := http.NewRequest("GET", "https://yt-api.p.rapidapi.com/dl?id="+videoID+"&cgeo=DE", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Add("x-rapidapi-host", "yt-api.p.rapidapi.com")
	req.Header.Add("x-rapidapi-key", apiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	var ytResp YouTubeResponse
	if err := json.NewDecoder(resp.Body).Decode(&ytResp); err != nil {
		return nil, "", fmt.Errorf("failed to parse API response: %v", err)
	}

	if !ytResp.Success || ytResp.Url == "" {
		return nil, "", fmt.Errorf("API failed to return download URL")
	}

	// Download video to local file
	filename := filepath.Join(downloadsDir, sanitizeFilename(ytResp.Title)+".mp4")
	out, err := os.Create(filename)
	if err != nil {
		return nil, "", err
	}
	defer out.Close()

	videoResp, err := http.Get(ytResp.Url)
	if err != nil {
		return nil, "", err
	}
	defer videoResp.Body.Close()

	if _, err := io.Copy(out, videoResp.Body); err != nil {
		return nil, "", err
	}

	return []string{filename}, "video", nil
}

// ===================== HELPERS =====================
func sendMedia(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int, mediaType string) {
	caption := "@downloaderin123_bot orqali yuklab olindi"

	switch mediaType {
	case "video":
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.ReplyToMessageID = replyTo
		video.Caption = caption
		bot.Send(video)
	case "image":
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.ReplyToMessageID = replyTo
		photo.Caption = caption
		bot.Send(photo)
	}

	if err := os.Remove(filePath); err != nil {
		log.Printf("⚠️ Failed to delete file %s: %v", filePath, err)
	}
}

func extractYouTubeID(link string) string {
	// naive extraction
	if strings.Contains(link, "youtu.be/") {
		parts := strings.Split(link, "/")
		return parts[len(parts)-1]
	}
	if strings.Contains(link, "v=") {
		parts := strings.Split(link, "v=")
		id := strings.Split(parts[1], "&")[0]
		return id
	}
	return ""
}

func sanitizeFilename(name string) string {
	// remove invalid filesystem characters
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	return replacer.Replace(name)
}
