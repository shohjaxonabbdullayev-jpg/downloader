package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	ffmpegPath     = "ffmpeg"
	ytDlpPath      = "yt-dlp"
	galleryDlPath  = "gallery-dl"
	maxVideoHeight = 720
)

var (
	downloadsDir = "downloads"
	rapidAPIKey  string
	mutex        sync.Mutex // for single download at a time
)

func main() {
	_ = godotenv.Load()
	rapidAPIKey = os.Getenv("RAPIDAPI_KEY")
	if rapidAPIKey == "" {
		log.Fatal("❌ RAPIDAPI_KEY missing in .env")
	}

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
			fmt.Sprintf("👋 Salom %s!\n\n🎥 Menga Instagram, TikTok, Pinterest, Facebook yoki YouTube/X link yuboring – men videoni yoki rasmni yuklab beraman.",
				msg.From.FirstName)))
		return
	}

	links := extractLinks(text)
	if len(links) == 0 {
		return
	}

	// Lock to download links one by one
	mutex.Lock()
	defer mutex.Unlock()

	waitMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda..."))

	for _, link := range links {
		var files []string
		var mediaType string
		var err error

		if isYouTube(link) {
			files, mediaType, err = downloadYouTube(link)
		} else {
			files, mediaType, err = downloadOther(link)
		}

		if err != nil || len(files) == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Linkni tekshiring."))
			continue
		}

		for _, f := range files {
			sendMedia(bot, chatID, f, msg.MessageID, mediaType)
			os.Remove(f)
		}
	}

	// Delete loading message
	_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
		ChatID:    chatID,
		MessageID: waitMsg.MessageID,
	})
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
		strings.Contains(u, "twitter") ||
		strings.Contains(u, "x.com") ||
		isYouTube(u)
}

func isYouTube(link string) bool {
	return strings.Contains(link, "youtube.com/watch") || strings.Contains(link, "youtu.be/")
}

// ===================== DOWNLOAD YOUTUBE =====================
func downloadYouTube(link string) ([]string, string, error) {
	videoID := extractYouTubeID(link)
	if videoID == "" {
		return nil, "", fmt.Errorf("invalid YouTube link")
	}

	apiURL := fmt.Sprintf("https://youtube138.p.rapidapi.com/video/streaming-data/?id=%s", videoID)
	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("x-rapidapi-host", "youtube138.p.rapidapi.com")
	req.Header.Set("x-rapidapi-key", rapidAPIKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("API error: %s", string(body))
	}

	var data struct {
		Formats []struct {
			URL      string `json:"url"`
			Quality  string `json:"qualityLabel"`
			MimeType string `json:"mimeType"`
		} `json:"formats"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, "", err
	}

	// pick the highest quality mp4
	var downloadURL string
	for i := len(data.Formats) - 1; i >= 0; i-- {
		f := data.Formats[i]
		if strings.Contains(f.MimeType, "video/mp4") {
			downloadURL = f.URL
			break
		}
	}
	if downloadURL == "" {
		return nil, "", fmt.Errorf("no mp4 stream found")
	}

	// download video
	outFile := filepath.Join(downloadsDir, fmt.Sprintf("%s.mp4", videoID))
	out, err := os.Create(outFile)
	if err != nil {
		return nil, "", err
	}
	defer out.Close()

	resp2, err := http.Get(downloadURL)
	if err != nil {
		return nil, "", err
	}
	defer resp2.Body.Close()

	_, err = io.Copy(out, resp2.Body)
	if err != nil {
		return nil, "", err
	}

	return []string{outFile}, "video", nil
}

func extractYouTubeID(link string) string {
	re := regexp.MustCompile(`(?:v=|youtu\.be/)([\w-]+)`)
	m := re.FindStringSubmatch(link)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// ===================== DOWNLOAD OTHER LINKS =====================
func downloadOther(link string) ([]string, string, error) {
	start := time.Now()
	out := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", time.Now().Unix()))
	args := []string{
		"--no-warnings",
		"-f", "bestvideo+bestaudio/best",
		"--merge-output-format", "mp4",
		"-o", out,
		link,
	}

	run(ytDlpPath, args...)

	files := recentFiles(start)
	if len(files) > 0 {
		return files, detectMediaType(files), nil
	}

	// fallback gallery-dl
	run(galleryDlPath, "-d", downloadsDir, link)
	files = recentFiles(start)
	if len(files) > 0 {
		return files, "image", nil
	}

	return nil, "", fmt.Errorf("download failed")
}

// ===================== UTILITIES =====================
func detectMediaType(files []string) string {
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" || ext == ".mov" || ext == ".mkv" {
			return "video"
		}
	}
	return "image"
}

func run(cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...)
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	err := c.Run()
	return buf.String(), err
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

	// ================= BUTTONS =================
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
