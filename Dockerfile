# ============================
# 🏗️ STAGE 1 — Build Go binary
# ============================
FROM golang:1.24.4 AS builder

WORKDIR /app

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends build-essential && \
    rm -rf /var/lib/apt/lists/*

# Download Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy app source
COPY . .

# Build Go binary
RUN go build -o downloader-bot .

# ==============================
# 🚀 STAGE 2 — Final lightweight image
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

# ✅ Install Python packages yt-dlp and gallery-dl in isolated venv
RUN python3 -m venv /opt/yt && \
    /opt/yt/bin/pip install --no-cache-dir yt-dlp gallery-dl && \
    ln -s /opt/yt/bin/yt-dlp /usr/local/bin/yt-dlp && \
    ln -s /opt/yt/bin/gallery-dl /usr/local/bin/gallery-dl

# Create app directory
WORKDIR /app

# Copy Go binary
COPY --from=builder /app/downloader-bot .
COPY twitter.txt ./twitter.txt
COPY facebook.txt ./facebook.txt
COPY instagram.txt ./instagram.txt
COPY youtube.txt ./youtube.txt
COPY pinterest.txt ./pinterest.txt
RUN mkdir -p downloads

# Environment variables
ENV PORT=10000
EXPOSE 10000

# Health check
HEALTHCHECK CMD curl -f http://localhost:${PORT}/health || exit 1

# Run bot
CMD ["/app/downloader-bot"]
