# ============================
# 🏗️ STAGE 1 — Build Go binary
# ============================
FROM golang:1.24.4 AS builder

WORKDIR /app

# Install minimal build tools
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential && \
    rm -rf /var/lib/apt/lists/*

# Download Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy entire source
COPY . .

# Build optimized binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o downloader-bot .


# ==============================
# 🚀 STAGE 2 — Final runtime image
# ==============================
FROM debian:bookworm-slim

WORKDIR /app

# Install runtime dependencies (minimal)
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg \
    python3 \
    python3-venv \
    python3-pip \
    curl \
    ca-certificates && \
    apt-get clean && rm -rf /var/lib/apt/lists/*


# ============================
# 📦 Install yt-dlp + gallery-dl
# ============================

RUN python3 -m venv /opt/yt && \
    /opt/yt/bin/pip install --no-cache-dir yt-dlp gallery-dl && \
    ln -s /opt/yt/bin/yt-dlp /usr/local/bin/yt-dlp && \
    ln -s /opt/yt/bin/gallery-dl /usr/local/bin/gallery-dl


# ====================================
# 🗂 Copy binary & static template files
# ====================================
COPY --from=builder /app/downloader-bot .
COPY *.txt ./

# Create downloads directory
RUN mkdir -p downloads && chmod -R 777 downloads


# =======================
# 🌍 Environment & health
# =======================
ENV PORT=10000
EXPOSE 10000

HEALTHCHECK CMD curl -f http://localhost:${PORT}/health || exit 1


# =======================
# ▶️ Start the bot
# =======================
CMD ["/app/downloader-bot"]
