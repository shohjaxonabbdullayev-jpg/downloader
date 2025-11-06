package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const downloadsDir = "downloads"

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
		log.Fatalf("❌ Bot initialization failed: %v", err)
	}

	log.Printf("🤖 Bot authorized as @%s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			go handleMessage(bot, update.Message)
		}
	}
}

// ===================== HEALTH CHECK =====================
func startHealthCheckServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "OK")
	})

	log.Printf("💚 Health check server running on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Health check server failed: %v", err)
	}
}

// ===================== MESSAGE HANDLER =====================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	chatID := msg.Chat.ID

	switch text {
	case "/start":
		startMsg := fmt.Sprintf(
			"👋 Salom %s!\n\n🎥 Menga YouTube, Instagram yoki Pinterest link yuboring — men sizga videoni yoki rasmni yuboraman.",
			msg.From.FirstName,
		)
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return
	case "/help":
		helpMsg := "❓ Yordam kerak bo'lsa @nonfindable1 ga murojaat qiling."
		bot.Send(tgbotapi.NewMessage(chatID, helpMsg))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		return
	}

	for _, link := range links {
		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda... iltimos kuting.")
		loadingMsg.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(loadingMsg)

		go func(url string, chatID int64, replyToID, loadingMsgID int) {
			files, mediaType, err := downloadMedia(url)

			// Delete loading message
			bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", url, err)
				errorMsg := tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ Yuklab bo‘lmadi: %v", err))
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

			for _, file := range files {
				sendMediaAndAttachShareButtons(bot, chatID, file, replyToID, mediaType)
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
		strings.Contains(text, "pinterest.com") ||
		strings.Contains(text, "pin.it")
}

// ===================== DOWNLOAD MEDIA =====================
func downloadMedia(link string) ([]string, string, error) {
	if strings.Contains(link, "youtube.com") || strings.Contains(link, "youtu.be") {
		return downloadYouTube(link)
	}

	// Keep Instagram/Pinterest as-is if needed
	return nil, "", fmt.Errorf("unsupported link")
}

// ===================== YOUTUBE DOWNLOAD VIA RAPIDAPI =====================
type YouTubeAPIResponse struct {
	Success bool   `json:"success"`
	Title   string `json:"title"`
	Url     string `json:"url"`
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

	req, _ := http.NewRequest("GET", "https://yt-api.p.rapidapi.com/dl?id="+videoID+"&cgeo=DE", nil)
	req.Header.Add("x-rapidapi-host", "yt-api.p.rapidapi.com")
	req.Header.Add("x-rapidapi-key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	var apiResp YouTubeAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, "", fmt.Errorf("failed to parse API response: %v", err)
	}

	if !apiResp.Success || apiResp.Url == "" {
		return nil, "", fmt.Errorf("API failed to return download URL")
	}

	filename := filepath.Join(downloadsDir, sanitizeFilename(apiResp.Title)+".mp4")
	out, err := os.Create(filename)
	if err != nil {
		return nil, "", err
	}
	defer out.Close()

	videoResp, err := http.Get(apiResp.Url)
	if err != nil {
		return nil, "", err
	}
	defer videoResp.Body.Close()

	if _, err := io.Copy(out, videoResp.Body); err != nil {
		return nil, "", err
	}

	return []string{filename}, "video", nil
}

func extractYouTubeID(link string) string {
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
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_",
	)
	return replacer.Replace(name)
}

// ===================== SEND MEDIA =====================
func sendMediaAndAttachShareButtons(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int, mediaType string) error {
	caption := "@downloaderin123_bot orqali yuklab olindi"
	switch mediaType {
	case "video":
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.ReplyToMessageID = replyTo
		video.Caption = caption
		if _, err := bot.Send(video); err != nil {
			return err
		}
	case "image":
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.ReplyToMessageID = replyTo
		photo.Caption = caption
		if _, err := bot.Send(photo); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown media type: %s", mediaType)
	}

	if err := os.Remove(filePath); err != nil {
		log.Printf("⚠️ Failed to delete file %s: %v", filePath, err)
	} else {
		log.Printf("🗑️ Deleted file %s after sending", filePath)
	}
	return nil
}
