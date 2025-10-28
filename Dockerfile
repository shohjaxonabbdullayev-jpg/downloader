# ============================
# 🏗️ STAGE 1 — Build the Go app
# ============================
FROM golang:1.24.4 AS builder

# Set working directory
WORKDIR /app

# Install build tools (some Telegram deps require C)
RUN apt-get update && \
    apt-get install -y --no-install-recommends build-essential && \
    rm -rf /var/lib/apt/lists/*

# Copy dependency files first (for caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the binary
RUN go build -o downloader-bot .

# ==============================
# 🚀 STAGE 2 — Final lightweight image
# ==============================
FROM debian:bookworm-slim

# Install runtime dependencies (ffmpeg, yt-dlp)
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    ffmpeg \
    python3 \
    python3-pip \
    ca-certificates && \
    pip3 install --no-cache-dir yt-dlp && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/downloader-bot .

# Copy optional files if available
COPY cookies.txt ./cookies.txt
COPY downloads ./downloads

# Render automatically injects environment variables
# If running locally, use: docker run --env-file .env downloader
ENV PORT=10000
EXPOSE 10000

# Run the bot
CMD ["/app/downloader-bot"]
