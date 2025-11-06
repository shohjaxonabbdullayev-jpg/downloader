package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

var downloadsDir = "downloads"
var sem = make(chan struct{}, 3) // limit concurrent downloads

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
			"👋 Salom %s!\n\n🎥 Menga YouTube yoki Instagram link yuboring — men sizga videoni yuboraman.",
			msg.From.FirstName,
		)
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return
	case "/help":
		helpMsg := "❓ Yordam kerak bo'lsa adminga murojaat qiling."
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
			sem <- struct{}{}
			files, mediaType, err := downloadMedia(url)
			<-sem

			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
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
		strings.Contains(text, "instagr.am")
}

// ===================== DOWNLOAD MEDIA =====================
func downloadMedia(link string) ([]string, string, error) {
	start := time.Now()
	if strings.Contains(link, "youtube.com") || strings.Contains(link, "youtu.be") {
		return downloadYouTube(link, start)
	} else if strings.Contains(link, "instagram.com") || strings.Contains(link, "instagr.am") {
		return downloadInstagram(link, start)
	}
	return nil, "", fmt.Errorf("unsupported link")
}

// ===================== YOUTUBE (RapidAPI) =====================
func downloadYouTube(videoURL string, start time.Time) ([]string, string, error) {
	apiKey := os.Getenv("RAPIDAPI_KEY")
	if apiKey == "" {
		return nil, "", fmt.Errorf("❌ RAPIDAPI_KEY not set in .env")
	}

	reqURL := fmt.Sprintf(
		"https://youtube-downloader-api-fast-reliable-and-easy.p.rapidapi.com/fetch_video?url=%s",
		url.QueryEscape(videoURL),
	)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, "", err
	}

	req.Header.Set("x-rapidapi-host", "youtube-downloader-api-fast-reliable-and-easy.p.rapidapi.com")
	req.Header.Set("x-rapidapi-key", apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("⚠️ RapidAPI returned status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, "", fmt.Errorf("failed to parse RapidAPI response: %v", err)
	}

	// The API returns video URLs under "video" key
	videoData, ok := result["video"].(map[string]interface{})
	if !ok {
		return nil, "", fmt.Errorf("no video found in response")
	}

	downloadURL, ok := videoData["url"].(string)
	if !ok || downloadURL == "" {
		return nil, "", fmt.Errorf("invalid video URL")
	}

	filePath := filepath.Join(downloadsDir, fmt.Sprintf("%d_youtube.mp4", time.Now().UnixNano()))
	outFile, err := os.Create(filePath)
	if err != nil {
		return nil, "", err
	}
	defer outFile.Close()

	respVideo, err := http.Get(downloadURL)
	if err != nil {
		return nil, "", err
	}
	defer respVideo.Body.Close()
	io.Copy(outFile, respVideo.Body)

	return []string{filePath}, "video", nil
}

// ===================== INSTAGRAM (RapidAPI) =====================
func downloadInstagram(instaURL string, start time.Time) ([]string, string, error) {
	apiKey := os.Getenv("RAPIDAPI_KEY")
	if apiKey == "" {
		return nil, "", fmt.Errorf("❌ RAPIDAPI_KEY not set in .env")
	}

	reqURL := fmt.Sprintf(
		"https://instagram-downloader-download-instagram-videos-stories1.p.rapidapi.com/?Userinfo=%s",
		url.QueryEscape(instaURL),
	)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, "", err
	}

	req.Header.Set("x-rapidapi-host", "instagram-downloader-download-instagram-videos-stories1.p.rapidapi.com")
	req.Header.Set("x-rapidapi-key", apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("⚠️ RapidAPI returned status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, "", fmt.Errorf("failed to parse RapidAPI response: %v", err)
	}

	mediaURLs, ok := result["media"].([]interface{})
	if !ok || len(mediaURLs) == 0 {
		return nil, "", fmt.Errorf("no media found in Instagram response")
	}

	var downloadedFiles []string
	for i, m := range mediaURLs {
		urlStr, ok := m.(string)
		if !ok || urlStr == "" {
			continue
		}
		filePath := filepath.Join(downloadsDir, fmt.Sprintf("%d_insta_%d.mp4", time.Now().UnixNano(), i))
		outFile, err := os.Create(filePath)
		if err != nil {
			continue
		}

		respVideo, err := http.Get(urlStr)
		if err != nil {
			outFile.Close()
			continue
		}
		io.Copy(outFile, respVideo.Body)
		outFile.Close()
		respVideo.Body.Close()

		downloadedFiles = append(downloadedFiles, filePath)
	}

	if len(downloadedFiles) == 0 {
		return nil, "", fmt.Errorf("no files downloaded from Instagram")
	}

	return downloadedFiles, "video", nil
}

// ===================== SEND MEDIA WITH SHARE BUTTONS + DELETE =====================
func sendMediaAndAttachShareButtons(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int, mediaType string) error {
	caption := "@downloaderin123_bot orqali yuklab olindi"
	var err error

	switch mediaType {
	case "video":
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.ReplyToMessageID = replyTo
		video.Caption = caption
		_, err = bot.Send(video)
	case "image":
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.ReplyToMessageID = replyTo
		photo.Caption = caption
		_, err = bot.Send(photo)
	default:
		return fmt.Errorf("unknown media type: %s", mediaType)
	}

	if err != nil {
		return fmt.Errorf("failed to send media: %w", err)
	}

	// Delete file after sending
	if err := os.Remove(filePath); err != nil {
		log.Printf("⚠️ Failed to delete file %s: %v", filePath, err)
	} else {
		log.Printf("🗑️ Deleted file %s after sending", filePath)
	}

	return nil
}
