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
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	ffmpegPath     = "ffmpeg"
	ytDlpPath      = "yt-dlp"
	maxVideoHeight = 480
)

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 3) // concurrency limit
)

const (
	rapidAPIInstagramKey = "e8ca5c51fcmsh1fe3e62d1239314p13f76cjsnfba3e0644676"
	downweeAPIKey        = "e8ca5c51fcmsh1fe3e62d1239314p13f76cjsnfba3e0644676"
	universalAPIKey      = "e8ca5c51fcmsh1fe3e62d1239314p13f76cjsnfba3e0644676"
)

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

	os.MkdirAll(downloadsDir, 0755)

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("🤖 Bot running as @%s", bot.Self.UserName)

	// Health check server
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprint(w, "OK")
		})
		log.Printf("💚 Health check server running on port %s", port)
		log.Fatal(http.ListenAndServe(":"+port, nil))
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

// ===================== HANDLE MESSAGES =====================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	chatID := msg.Chat.ID

	if text == "/start" {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf(
			"👋 Salom %s!\n\n🎥 YouTube, Instagram, Pinterest, TikTok, Facebook yoki Twitter/X link yuboring — men videoni yoki rasmni yuboraman.",
			msg.From.FirstName)))
		return
	}

	links := extractLinks(text)
	if len(links) == 0 {
		return
	}

	waitMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda..."))

	for _, link := range links {
		go func(l string) {
			sem <- struct{}{}
			files, mediaType, err := download(l)
			<-sem

			bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: waitMsg.MessageID})

			if err != nil || len(files) == 0 {
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Linkni tekshiring."))
				return
			}

			for _, f := range files {
				sendMedia(bot, chatID, f, msg.MessageID, mediaType)
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
		if isSupported(u) {
			out = append(out, u)
		}
	}
	return out
}

func isSupported(u string) bool {
	u = strings.ToLower(u)
	return strings.Contains(u, "youtube") ||
		strings.Contains(u, "youtu.be") ||
		strings.Contains(u, "instagram") ||
		strings.Contains(u, "instagr.am") ||
		strings.Contains(u, "pinterest") ||
		strings.Contains(u, "pin.it") ||
		strings.Contains(u, "tiktok") ||
		strings.Contains(u, "facebook") ||
		strings.Contains(u, "fb.watch") ||
		strings.Contains(u, "twitter.com") ||
		strings.Contains(u, "x.com")
}

// ===================== DOWNLOAD =====================
func download(link string) ([]string, string, error) {
	start := time.Now()

	// Instagram / Facebook via DownWee
	if strings.Contains(link, "facebook") || strings.Contains(link, "fb.watch") ||
		(strings.Contains(link, "instagram") && (strings.Contains(link, "/reel/") || strings.Contains(link, "/p/") || strings.Contains(link, "/stories/"))) {
		file, err := fetchDownweeVideo(link)
		if err != nil {
			return nil, "", fmt.Errorf("failed to download video: %v", err)
		}
		return []string{file}, "video", nil
	}

	// Twitter / X via Universal Social Media Downloader API
	if strings.Contains(link, "twitter.com") || strings.Contains(link, "x.com") {
		file, mediaType, err := fetchTwitterMedia(link)
		if err != nil {
			return nil, "", fmt.Errorf("failed to download Twitter/X media: %v", err)
		}
		return []string{file}, mediaType, nil
	}

	// YouTube / TikTok fallback via yt-dlp
	out := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", time.Now().Unix()))
	args := []string{"--no-warnings", "-f", fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best/best", maxVideoHeight), "--merge-output-format", "mp4", "-o", out, link}
	_, _ = run(ytDlpPath, args...)
	files := recentFiles(start)
	if len(files) > 0 {
		return files, "video", nil
	}

	return nil, "", fmt.Errorf("download failed")
}

// ===================== HELPERS =====================
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

func downloadFile(url, path string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// ===================== DOWNWEE =====================
type DownweeResponse struct {
	Data struct {
		Title    string `json:"title"`
		VideoURL string `json:"videoUrl"`
	} `json:"data"`
}

func fetchDownweeVideo(url string) (string, error) {
	apiURL := "https://downwee-video-downloader.p.rapidapi.com/download"
	payload := map[string]string{"url": url}
	data, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", apiURL, bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-rapidapi-host", "downwee-video-downloader.p.rapidapi.com")
	req.Header.Set("x-rapidapi-key", downweeAPIKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result DownweeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if result.Data.VideoURL == "" {
		return "", fmt.Errorf("video URL not found")
	}

	filename := filepath.Join(downloadsDir, filepath.Base(result.Data.VideoURL))
	out, err := os.Create(filename)
	if err != nil {
		return "", err
	}
	defer out.Close()

	resp2, err := http.Get(result.Data.VideoURL)
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()

	_, err = io.Copy(out, resp2.Body)
	if err != nil {
		return "", err
	}

	return filename, nil
}

// ===================== TWITTER / X =====================
func fetchTwitterMedia(url string) (string, string, error) {
	apiURL := fmt.Sprintf("https://universal-social-media-content-downloader-api.p.rapidapi.com/download?url=%s", url)

	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("x-rapidapi-host", "universal-social-media-content-downloader-api.p.rapidapi.com")
	req.Header.Set("x-rapidapi-key", universalAPIKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			VideoURL string `json:"videoUrl,omitempty"`
			ImageURL string `json:"imageUrl,omitempty"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", err
	}

	var mediaURL, mediaType string
	if result.Data.VideoURL != "" {
		mediaURL = result.Data.VideoURL
		mediaType = "video"
	} else if result.Data.ImageURL != "" {
		mediaURL = result.Data.ImageURL
		mediaType = "image"
	} else {
		return "", "", fmt.Errorf("no media URL found")
	}

	filename := filepath.Join(downloadsDir, filepath.Base(mediaURL))
	out, err := os.Create(filename)
	if err != nil {
		return "", "", err
	}
	defer out.Close()

	resp2, err := http.Get(mediaURL)
	if err != nil {
		return "", "", err
	}
	defer resp2.Body.Close()

	_, err = io.Copy(out, resp2.Body)
	if err != nil {
		return "", "", err
	}

	return filename, mediaType, nil
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
		log.Println("Send error:", err)
		return
	}

	btnShare := tgbotapi.NewInlineKeyboardButtonSwitch("📤 Ulashish", "")
	btnGroup := tgbotapi.NewInlineKeyboardButtonURL("👥 Guruhga qo‘shish", fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(btnShare),
		tgbotapi.NewInlineKeyboardRow(btnGroup),
	)

	bot.Send(tgbotapi.NewEditMessageReplyMarkup(chatID, msg.MessageID, keyboard))
}
