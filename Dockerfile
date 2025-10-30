# ============================
# 🏗️ STAGE 1 — Build Go binary
# ============================
FROM golang:1.24.4 AS builder

WORKDIR /app

# Install minimal tools (no Python needed here)
RUN apt-get update && apt-get install -y --no-install-recommends build-essential ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o downloader-bot .

# ==============================
# 🚀 STAGE 2 — Final lightweight image
# ==============================
FROM python:3.12-slim AS runtime

# Install ffmpeg and system dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends ffmpeg ca-certificates && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# ✅ Install all Python tools here
RUN pip install --no-cache-dir yt-dlp gallery-dl instaloader

WORKDIR /app

# Copy compiled Go binary and resources
COPY --from=builder /app/downloader-bot .
COPY cookies.txt ./cookies.txt
COPY downloads ./downloads

# Health check & default port
ENV PORT=10000
EXPOSE 10000

CMD ["/app/downloader-bot"]
