# ============================
# 🏗️ STAGE 1 — Build Go binary
# ============================
FROM golang:1.24.4 AS builder

WORKDIR /app

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends build-essential && \
    rm -rf /var/lib/apt/lists/*

# Copy Go module files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the Go binary
RUN go build -o downloader-bot .

# ==============================
# 🚀 STAGE 2 — Final runtime image
# ==============================
FROM debian:bookworm-slim

# Install runtime dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ffmpeg \
        python3-full \
        python3-pip \
        ca-certificates \
        curl \
        wget \
        git && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# ✅ Install yt-dlp, gallery-dl, and requests (for yt1s_dl.py)
RUN pip3 install --no-cache-dir yt-dlp gallery-dl requests

# Create working directory
WORKDIR /app

# Copy Go binary and Python downloader script
COPY --from=builder /app/downloader-bot ./
COPY yt1s_dl.py ./yt1s_dl.py

# Copy optional cookies (only Instagram & Pinterest)
COPY instagram.txt ./instagram.txt
COPY pinterest.txt ./pinterest.txt

# Create downloads folder
RUN mkdir -p /app/downloads

# Environment variables
ENV PORT=10000
EXPOSE 10000

# Health check (optional)
HEALTHCHECK CMD curl -f http://localhost:${PORT}/health || exit 1

# Run the Telegram bot
CMD ["/app/downloader-bot"]
