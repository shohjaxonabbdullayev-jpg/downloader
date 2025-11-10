package main

import (
	"bytes"
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

var downloadsDir = "downloads"
var sem = make(chan struct{}, 3)

const instagramAPIKey = "e8ca5c51fcmsh1fe3e62d1239314p13f76cjsnfba3e0644676"

// ===================== MAIN =====================
func main() {
	_ = godotenv.Load()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN missing")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	webhookURL := os.Getenv("WEBHOOK_URL") // must be HTTPS, e.g., https://yourdomain.com/<token>
	if webhookURL == "" {
		log.Fatal("❌ WEBHOOK_URL missing")
	}

	os.MkdirAll(downloadsDir, 0755)

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("🤖 Bot running as @%s", bot.Self.UserName)

	// Set webhook

	updates := bot.ListenForWebhook("/" + bot.Token)
	go func() {
		for update := range updates {
			handleUpdate(bot, update)
		}
	}()

	// Health check
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	log.Printf("💚 Server running on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// ===================== HANDLE UPDATE =====================
func handleUpdate(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	if update.Message == nil {
		return
	}

	text := strings.TrimSpace(update.Message.Text)
	chatID := update.Message.Chat.ID

	if text == "/start" {
		bot.Send(tgbotapi.NewMessage(chatID, "👋 Salom! Instagram username yoki link yuboring."))
		return
	}

	links := extractLinks(text)
	if len(links) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Iltimos, Instagram linkini yuboring."))
		return
	}

	waitMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda..."))

	for _, link := range links {
		go func(l string) {
			sem <- struct{}{}
			files, mediaType, err := downloadInstagramMedia(l)
			<-sem

			bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: waitMsg.MessageID})

			if err != nil || len(files) == 0 {
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Linkni tekshiring."))
				return
			}

			for _, f := range files {
				sendMedia(bot, chatID, f, update.Message.MessageID, mediaType)
				os.Remove(f)
			}
		}(link)
	}
}

// ===================== LINK PARSING =====================
func extractLinks(text string) []string {
	re := regexp.MustCompile(`https?://\S+`)
	raw := re.FindAllString(text, -1)
	var out []string
	for _, u := range raw {
		if strings.Contains(u, "instagram.com") || strings.Contains(u, "instagr.am") {
			out = append(out, u)
		}
	}
	return out
}

// ===================== DOWNLOAD INSTAGRAM =====================
func downloadInstagramMedia(link string) ([]string, string, error) {
	username := extractUsername(link)

	postFiles, _ := fetchInstagramPosts(username)
	storyFiles, _ := fetchInstagramStories(username)

	files := append(postFiles, storyFiles...)
	mediaType := "image"
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" {
			mediaType = "video"
			break
		}
	}

	if len(files) == 0 {
		return nil, "", fmt.Errorf("no media found")
	}

	return files, mediaType, nil
}

func extractUsername(link string) string {
	link = strings.Trim(link, "/")
	parts := strings.Split(link, "/")
	for i, p := range parts {
		if p == "p" || p == "reel" || p == "tv" {
			if i > 0 {
				return parts[i-1]
			}
		}
	}
	return parts[len(parts)-1]
}

// ===================== FETCH POSTS =====================
func fetchInstagramPosts(username string) ([]string, error) {
	apiURL := "https://instagram-api-cheapest-2026.p.rapidapi.com/posts"
	payload := map[string]string{"username": username}
	data, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", apiURL, bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-rapidapi-host", "instagram-api-cheapest-2026.p.rapidapi.com")
	req.Header.Set("x-rapidapi-key", instagramAPIKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Posts []struct {
			MediaURL string `json:"mediaUrl"`
		} `json:"posts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var files []string
	for _, p := range result.Posts {
		file := filepath.Join(downloadsDir, filepath.Base(p.MediaURL))
		if err := downloadFile(p.MediaURL, file); err == nil {
			files = append(files, file)
		}
	}
	return files, nil
}

// ===================== FETCH STORIES =====================
func fetchInstagramStories(username string) ([]string, error) {
	apiURL := "https://instagram-api-cheapest-2026.p.rapidapi.com/stories"
	payload := map[string]string{"username": username}
	data, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", apiURL, bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-rapidapi-host", "instagram-api-cheapest-2026.p.rapidapi.com")
	req.Header.Set("x-rapidapi-key", instagramAPIKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Stories []struct {
			MediaURL string `json:"mediaUrl"`
		} `json:"stories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var files []string
	for _, s := range result.Stories {
		file := filepath.Join(downloadsDir, filepath.Base(s.MediaURL))
		if err := downloadFile(s.MediaURL, file); err == nil {
			files = append(files, file)
		}
	}
	return files, nil
}

// ===================== DOWNLOAD FILE =====================
func downloadFile(url, path string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// ===================== SEND MEDIA =====================
func sendMedia(bot *tgbotapi.BotAPI, chatID int64, file string, replyTo int, mediaType string) {
	caption := "@downloaderin123_bot orqali yuklab olindi"

	if mediaType == "video" {
		v := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(file))
		v.Caption = caption
		v.ReplyToMessageID = replyTo
		bot.Send(v)
	} else {
		p := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(file))
		p.Caption = caption
		p.ReplyToMessageID = replyTo
		bot.Send(p)
	}
}
