# ============================
# 🏗️ Stage 1 — Build Go binary
# ============================
FROM golang:1.24.4 AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o downloader-bot .

# ==============================
# 🚀 Stage 2 — Lightweight runtime
# ==============================
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    chromium ffmpeg python3-full python3-pip ca-certificates curl wget git && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

RUN python3 -m venv /opt/yt && \
    /opt/yt/bin/pip install --no-cache-dir yt-dlp gallery-dl && \
    ln -s /opt/yt/bin/yt-dlp /usr/local/bin/yt-dlp && \
    ln -s /opt/yt/bin/gallery-dl /usr/local/bin/gallery-dl

WORKDIR /app
COPY --from=builder /app/downloader-bot .

RUN mkdir -p downloads
ENV PORT=10000
EXPOSE 10000
HEALTHCHECK CMD curl -f http://localhost:${PORT}/health || exit 1

CMD ["/app/downloader-bot"]
