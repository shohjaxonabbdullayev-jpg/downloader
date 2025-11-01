# ============================
# 🏗️ STAGE 1 — Build Go binary
# ============================
FROM golang:1.24.4 AS builder

WORKDIR /app

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends build-essential && \
    rm -rf /var/lib/apt/lists/*

# Copy and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and build
COPY . .
RUN go build -o downloader-bot .

# ==============================
# 🚀 STAGE 2 — Final runtime image
# ==============================
FROM debian:bookworm-slim

# Install runtime dependencies (including Chromium for chromedp)
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ffmpeg \
        python3-full \
        python3-pip \
        ca-certificates \
        curl \
        wget \
        git \
        fonts-liberation \
        libasound2 \
        libatk1.0-0 \
        libc6 \
        libcairo2 \
        libcups2 \
        libdbus-1-3 \
        libexpat1 \
        libfontconfig1 \
        libgbm1 \
        libglib2.0-0 \
        libgtk-3-0 \
        libnspr4 \
        libnss3 \
        libpango-1.0-0 \
        libx11-6 \
        libx11-xcb1 \
        libxcb1 \
        libxcomposite1 \
        libxcursor1 \
        libxdamage1 \
        libxext6 \
        libxfixes3 \
        libxi6 \
        libxrandr2 \
        libxrender1 \
        libxss1 \
        libxtst6 \
        chromium && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# ✅ Set CHROME_PATH for chromedp (important)
ENV CHROME_PATH=/usr/bin/chromium

# ✅ Install yt-dlp & gallery-dl (in isolated venv)
RUN python3 -m venv /opt/yt && \
    /opt/yt/bin/pip install --no-cache-dir yt-dlp gallery-dl && \
    ln -s /opt/yt/bin/yt-dlp /usr/local/bin/yt-dlp && \
    ln -s /opt/yt/bin/gallery-dl /usr/local/bin/gallery-dl

# App setup
WORKDIR /app
COPY --from=builder /app/downloader-bot .

# Create downloads folder (used by the bot)
RUN mkdir -p downloads

# Environment variables
ENV PORT=10000
EXPOSE 10000

# Health check
HEALTHCHECK CMD curl -f http://localhost:${PORT}/health || exit 1

# Run bot
CMD ["/app/downloader-bot"]
