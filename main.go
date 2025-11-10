package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	ytDlpPath      = "yt-dlp"
	maxVideoHeight = 720
	cookieFile     = "youtube.txt"
)

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 3)
	profileDir   = "./chrome-data"
)

func main() {
	_ = godotenv.Load()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("BOT_TOKEN missing")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	os.MkdirAll(downloadsDir, 0755)
	os.MkdirAll(profileDir, 0755)

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("🤖 Bot running as @%s", bot.Self.UserName)

	// Health check
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprint(w, "OK")
		})
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
			"👋 Salom %s!\n\n🎥 YouTube link yuboring — men videoni yuklab beraman.",
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
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi: "+err.Error()))
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
		u = strings.ToLower(u)
		if strings.Contains(u, "youtube") || strings.Contains(u, "youtu.be") ||
			strings.Contains(u, "instagram") || strings.Contains(u, "tiktok") ||
			strings.Contains(u, "facebook") || strings.Contains(u, "twitter") {
			out = append(out, u)
		}
	}
	return out
}

// ===================== DOWNLOAD =====================
func download(link string) ([]string, string, error) {
	start := time.Now()

	// Ensure youtube cookies exist
	if strings.Contains(link, "youtube") || strings.Contains(link, "youtu.be") {
		if !fileExists(cookieFile) || fileOlderThan(cookieFile, 24*time.Hour) {
			log.Println("✅ Exporting cookies from Chromium profile")
			if err := exportCookiesFromProfile(profileDir, cookieFile); err != nil {
				log.Println("❌ Failed to export cookies:", err)
			}
		}
	}

	out := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", time.Now().Unix()))
	args := []string{
		"--no-warnings",
		"--geo-bypass", // ✅ bypass region restrictions
		"-f", fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best/best", maxVideoHeight),
		"--merge-output-format", "mp4",
		"-o", out,
		link,
	}

	if strings.Contains(link, "youtube") || strings.Contains(link, "youtu.be") {
		args = append([]string{"--cookies", cookieFile}, args...)
	}

	outStr, err := run(ytDlpPath, args...)
	if err != nil {
		log.Println("yt-dlp failed:", err)
		log.Println("yt-dlp output:", outStr)
	}

	files := recentFiles(start)
	if len(files) > 0 {
		return files, detectMediaType(files), nil
	}

	return nil, "", fmt.Errorf("download failed; yt-dlp: %v", err)
}

// ===================== EXPORT COOKIES =====================
func exportCookiesFromProfile(profileDir, path string) error {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.UserDataDir(profileDir),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Navigate YouTube to load session
	if err := chromedp.Run(ctx,
		chromedp.Navigate("https://www.youtube.com/"),
		chromedp.Sleep(2*time.Second),
	); err != nil {
		return err
	}

	// Enable network and get cookies
	if err := chromedp.Run(ctx, network.Enable()); err != nil {
		return fmt.Errorf("network.Enable failed: %w", err)
	}
	cookies, err := network.GetCookies().Do(ctx)
	if err != nil {
		return fmt.Errorf("network.GetAllCookies failed: %w", err)
	}

	return writeNetscapeCookies(path, cookies)
}

// write cookies in Netscape format for yt-dlp
func writeNetscapeCookies(path string, cookies []*network.Cookie) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintln(f, "# Netscape HTTP Cookie File")
	for _, c := range cookies {
		domain := c.Domain
		if !strings.HasPrefix(domain, ".") {
			domain = "." + domain
		}
		flag := "FALSE"
		if strings.HasPrefix(c.Domain, ".") {
			flag = "TRUE"
		}
		secure := "FALSE"
		if c.Secure {
			secure = "TRUE"
		}
		exp := int64(c.Expires)
		if exp == 0 {
			exp = time.Now().Add(7 * 24 * time.Hour).Unix()
		}
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%s\t%s",
			domain, flag, c.Path, secure, exp, c.Name, c.Value)
		fmt.Fprintln(f, line)
	}
	return nil
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

func detectMediaType(files []string) string {
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" || ext == ".mov" {
			return "video"
		}
	}
	return "image"
}

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

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func fileOlderThan(path string, d time.Duration) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return time.Since(info.ModTime()) > d
}
