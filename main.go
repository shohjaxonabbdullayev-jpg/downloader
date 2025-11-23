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
	"sort"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	downloadsDir = "downloads"
	rapidAPIKey  = "e8ca5c51fcmsh1fe3e62d1239314p13f76cjsnfba3e0644676"
)

var sem = make(chan struct{}, 1) // sequential downloads

func main() {
	_ = godotenv.Load()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN missing in .env")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	os.MkdirAll(downloadsDir, 0755)

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("🤖 Bot started as @%s", bot.Self.UserName)

	// Health check
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("OK"))
		})
		log.Printf("💚 Health check on port %s", port)
		http.ListenAndServe(":"+port, nil)
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			go handleMessage(bot, update.Message)
		}
	}
}

// ===================== HANDLE MESSAGE =====================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)

	if text == "/start" {
		bot.Send(tgbotapi.NewMessage(chatID,
			fmt.Sprintf("👋 Salom %s!\n\n🎥 Menga link yuboring – men videoni yoki rasmni yuklab beraman.",
				msg.From.FirstName)))
		return
	}

	links := extractLinks(text)
	if len(links) == 0 {
		return
	}

	waitMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda..."))

	for _, link := range links {
		sem <- struct{}{} // lock sequential download
		files, mediaType, err := download(link)
		<-sem

		_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
			ChatID:    chatID,
			MessageID: waitMsg.MessageID,
		})

		if err != nil || len(files) == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ Yuklab bo‘lmadi: %s", link)))
			continue
		}

		for _, f := range files {
			sendMedia(bot, chatID, f, msg.MessageID, mediaType)
			os.Remove(f)
		}
	}
}

// ===================== LINK PARSING =====================
func extractLinks(text string) []string {
	re := regexp.MustCompile(`https?://\S+`)
	found := re.FindAllString(text, -1)
	var out []string
	for _, u := range found {
		if isSupported(u) {
			out = append(out, u)
		}
	}
	return out
}

func isSupported(u string) bool {
	u = strings.ToLower(u)
	return strings.Contains(u, "instagram") ||
		strings.Contains(u, "tiktok") ||
		strings.Contains(u, "pinterest") ||
		strings.Contains(u, "facebook") ||
		strings.Contains(u, "fb.watch") ||
		strings.Contains(u, "youtube") ||
		strings.Contains(u, "youtu.be") ||
		strings.Contains(u, "twitter") ||
		strings.Contains(u, "x.com")
}

// ===================== DOWNLOAD =====================
func download(link string) ([]string, string, error) {
	start := time.Now()

	if strings.Contains(link, "youtube") || strings.Contains(link, "youtu.be") {
		return downloadYouTube(link)
	}

	// fallback yt-dlp or gallery-dl for other links
	return downloadOther(link, start)
}

// ===================== YOUTUBE DOWNLOAD (YTSTREAM RAPIDAPI) =====================
func downloadYouTube(link string) ([]string, string, error) {
	videoID := extractYouTubeID(link)
	if videoID == "" {
		return nil, "", fmt.Errorf("invalid YouTube link")
	}

	url := fmt.Sprintf("https://ytstream-download-youtube-videos.p.rapidapi.com/dl?id=%s", videoID)

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Add("x-rapidapi-host", "ytstream-download-youtube-videos.p.rapidapi.com")
	req.Header.Add("x-rapidapi-key", rapidAPIKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("RapidAPI error: %s", resp.Status)
	}

	var data struct {
		Formats []struct {
			URL      string `json:"url"`
			Quality  string `json:"quality"`
			MimeType string `json:"mimeType"`
		} `json:"formats"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, "", err
	}

	// Pick 720p mp4 if available
	var downloadURL string
	for _, f := range data.Formats {
		if f.Quality == "720p" && strings.Contains(f.MimeType, "video/mp4") {
			downloadURL = f.URL
			break
		}
	}
	if downloadURL == "" && len(data.Formats) > 0 {
		// fallback to highest quality available
		downloadURL = data.Formats[len(data.Formats)-1].URL
	}

	// download file
	filename := filepath.Join(downloadsDir, videoID+".mp4")
	outFile, err := os.Create(filename)
	if err != nil {
		return nil, "", err
	}
	defer outFile.Close()

	resp2, err := http.Get(downloadURL)
	if err != nil {
		return nil, "", err
	}
	defer resp2.Body.Close()

	_, err = io.Copy(outFile, resp2.Body)
	if err != nil {
		return nil, "", err
	}

	return []string{filename}, "video", nil
}

func extractYouTubeID(link string) string {
	re := regexp.MustCompile(`(?:v=|youtu\.be/|shorts/)([a-zA-Z0-9_-]{11})`)
	m := re.FindStringSubmatch(link)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// ===================== OTHER DOWNLOADS =====================
func downloadOther(link string, start time.Time) ([]string, string, error) {
	out := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", time.Now().Unix()))
	run("yt-dlp", "-f", "bestvideo+bestaudio/best", "--merge-output-format", "mp4", "-o", out, link)

	files := recentFiles(start)
	if len(files) > 0 {
		return files, detectMediaType(files), nil
	}

	run("gallery-dl", "-d", downloadsDir, link)
	files = recentFiles(start)
	if len(files) > 0 {
		return files, "image", nil
	}

	return nil, "", fmt.Errorf("download failed")
}

func detectMediaType(files []string) string {
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" || ext == ".mov" || ext == ".mkv" {
			return "video"
		}
	}
	return "image"
}

func recentFiles(since time.Time) []string {
	var files []string
	filepath.Walk(downloadsDir, func(p string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() && info.ModTime().After(since) {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// ===================== SEND MEDIA =====================
func sendMedia(bot *tgbotapi.BotAPI, chatID int64, file string, replyTo int, mediaType string) {
	caption := "@downloaderin123_bot orqali yuklab olindi"

	var msg tgbotapi.Message
	var err error

	if mediaType == "video" {
		v := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(file))
		v.Caption = caption
		v.ReplyToMessageID = replyTo
		msg, err = bot.Send(v)
	} else {
		p := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(file))
		p.Caption = caption
		p.ReplyToMessageID = replyTo
		msg, err = bot.Send(p)
	}

	if err != nil {
		log.Println("send error:", err)
		return
	}

	btnShare := tgbotapi.NewInlineKeyboardButtonURL(
		"📤 Do‘stlar bilan ulashish",
		fmt.Sprintf("https://t.me/%s", bot.Self.UserName),
	)
	btnGroup := tgbotapi.NewInlineKeyboardButtonURL(
		"👥 Guruhga qo‘shish",
		fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName),
	)
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(btnShare),
		tgbotapi.NewInlineKeyboardRow(btnGroup),
	)
	bot.Send(tgbotapi.NewEditMessageReplyMarkup(chatID, msg.MessageID, kb))
}

