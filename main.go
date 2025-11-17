package main

import (
    "bytes"
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

    tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
    "github.com/joho/godotenv"
)

const (
    ytDlpPath     = "yt-dlp"
    galleryDlPath = "gallery-dl"
)

var (
    downloadsDir = "downloads"
    sem          = make(chan struct{}, 3)
)

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
            fmt.Fprint(w, "OK")
        })
        http.ListenAndServe(":"+port, nil)
    }()

    // Auto refresh YouTube cookies
    go autoRefreshYouTubeCookies()

    updates := bot.GetUpdatesChan(tgbotapi.NewUpdate(0))

    for update := range updates {
        if update.Message != nil {
            go handleMessage(bot, update.Message)
        }
    }
}

// Auto refresh YouTube cookies every 6 hours
func autoRefreshYouTubeCookies() {
    for {
        log.Println("🔄 Refreshing YouTube cookies...")
        run(ytDlpPath, "--cookies-from-browser", "chrome", "--cookies", "youtube.txt", "https://www.youtube.com")
        log.Println("✅ YouTube cookies refreshed.")
        time.Sleep(6 * time.Hour)
    }
}

func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
    text := strings.TrimSpace(msg.Text)
    chatID := msg.Chat.ID

    if text == "/start" {
        bot.Send(tgbotapi.NewMessage(chatID, "👋 Salom! Link yuboring — yuklab beraman (YouTube → 720p, boshqalar → maksimal sifat)."))
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
                bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi."))
                return
            }

            for _, file := range files {
                sendMedia(bot, chatID, file, msg.MessageID, mediaType)
                os.Remove(file)
            }
        }(link)
    }
}

// Extract links
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
    return strings.Contains(u, "youtube") || strings.Contains(u, "youtu.be") || strings.Contains(u, "instagram") || strings.Contains(u, "instagr.am") || strings.Contains(u, "tiktok") || strings.Contains(u, "pinterest") || strings.Contains(u, "pin.it") || strings.Contains(u, "facebook") || strings.Contains(u, "fb.watch") || strings.Contains(u, "twitter") || strings.Contains(u, "x.com")
}

// Download function
func download(link string) ([]string, string, error) {
    start := time.Now()

    out := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", time.Now().Unix()))

    var format string

    // YouTube → 720p only
    if strings.Contains(link, "youtube.com") || strings.Contains(link, "youtu.be") {
        format = "bestvideo[height=720]+bestaudio/best[height=720]"
    } else {
        // Other sites → max quality
        format = "bv*+ba/best"
    }

    args := []string{"--no-warnings", "-f", format, "--merge-output-format", "mp4", "-o", out, link}

    // YouTube cookies
    if strings.Contains(link, "youtube") && fileExists("youtube.txt") {
        args = append([]string{"--cookies", "youtube.txt"}, args...)
    }

    run(ytDlpPath, args...)

    files := recentFiles(start)
    if len(files) > 0 {
        mType := "image"
        for _, f := range files {
            if strings.HasSuffix(strings.ToLower(f), ".mp4") {
                mType = "video"
                break
            }
        }
        return files, mType, nil
    }

    // Fallback: gallery-dl for images
    run(galleryDlPath, "-d", downloadsDir, link)
    files = recentFiles(start)
    if len(files) > 0 {
        return files, "image", nil
    }

    return nil, "", fmt.Errorf("download failed")
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
        return
    }

    btnShare := tgbotapi.NewInlineKeyboardButtonSwitch("📤 Ulashish", "")
    btnGroup := tgbotapi.NewInlineKeyboardButtonURL("👥 Guruhga qo‘shish", fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName))

    kb := tgbotapi.NewInlineKeyboardMarkup(
        tgbotapi.NewInlineKeyboardRow(btnShare),
        tgbotapi.NewInlineKeyboardRow(btnGroup),
    )

    bot.Send(tgbotapi.NewEditMessageReplyMarkup(chatID, msg.MessageID, kb))
}

func fileExists(path string) bool {
    info, err := os.Stat(path)
    return err == nil && !info.IsDir()
}

