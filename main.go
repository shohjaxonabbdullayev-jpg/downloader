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
	galleryDlPath  = "gallery-dl"
	maxVideoHeight = 720
	downloadsDir   = "downloads"
	semLimit       = 3
)

var sem = make(chan struct{}, semLimit)

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
			"👋 Salom %s!\n\n🎥 YouTube (720p via API), Instagram (high-res), Pinterest, TikTok, Facebook yoki Twitter link yuboring — men videoni yoki rasmni yuboraman.",
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

	// Use RapidAPI for YouTube
	if strings.Contains(link, "youtube") || strings.Contains(link, "youtu.be") {
		file, err := downloadYouTubeViaAPI(link)
		if err != nil {
			return nil, "", err
		}
		return []string{file}, "video", nil
	}

	// Other platforms → yt-dlp / gallery-dl
	out := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", time.Now().Unix()))
	args := []string{"--no-warnings", "-f", "bestvideo+bestaudio/best", "--merge-output-format", "mp4", "-o", out, link}
	_, _ = run("yt-dlp", args...)

	files := recentFiles(start)
	if len(files) > 0 {
		mType := "image"
		for _, f := range files {
			ext := strings.ToLower(filepath.Ext(f))
			if ext == ".mp4" || ext == ".mov" {
				mType = "video"
				break
			}
		}
		return files, mType, nil
	}

	// Fallback: gallery-dl for images (Instagram, Twitter/X, Facebook, Pinterest)
	run(galleryDlPath, "-d", downloadsDir, link)
	files = recentFiles(start)
	if len(files) > 0 {
		return files, "image", nil
	}

	return nil, "", fmt.Errorf("download failed")
}

// ===================== DOWNLOAD YOUTUBE VIA RAPIDAPI =====================
func downloadYouTubeViaAPI(videoURL string) (string, error) {
	apiURL := "https://youtube-downloader-video.p.rapidapi.com/yt_stream"
	req, _ := http.NewRequest("POST", apiURL, nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("video_url", videoURL)
	req.Header.Set("x-rapidapi-host", "youtube-downloader-video.p.rapidapi.com")
	req.Header.Set("x-rapidapi-key", "e8ca5c51fcmsh1fe3e62d1239314p13f76cjsnfba3e0644676")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}

	// Expect "url" field with direct mp4 download
	urlStr, ok := data["url"].(string)
	if !ok || urlStr == "" {
		return "", fmt.Errorf("failed to get download URL from API")
	}

	// Download video to local file
	filePath := filepath.Join(downloadsDir, fmt.Sprintf("%d_youtube.mp4", time.Now().Unix()))
	outFile, err := os.Create(filePath)
	if err != nil {
		return "", err
	}
	defer outFile.Close()

	videoResp, err := http.Get(urlStr)
	if err != nil {
		return "", err
	}
	defer videoResp.Body.Close()

	_, err = io.Copy(outFile, videoResp.Body)
	if err != nil {
		return "", err
	}

	return filePath, nil
}

// ===================== EXECUTE COMMAND =====================
func run(cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...)
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	err := c.Run()
	return buf.String(), err
}

// ===================== RECENT FILES =====================
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

// ===================== HELPERS =====================
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
