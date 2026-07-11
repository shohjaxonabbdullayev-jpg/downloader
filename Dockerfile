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

# Build a static, portable Go binary (no glibc/runtime linkage surprises)
RUN CGO_ENABLED=0 GOOS=linux go build -o downloader-bot .

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

# ✅ Install yt-dlp + instaloader (Instagram photos) in an isolated venv.
# gallery-dl is intentionally NOT installed — it login-redirects on these
# platforms and was causing media download errors; the bot never invokes it.
# curl_cffi enables yt-dlp's browser impersonation (--impersonate), which lets us
# fetch public media from this datacenter IP without cookies by presenting a real
# browser TLS fingerprint. Without it yt-dlp still runs, just without impersonation.
RUN python3 -m venv /opt/yt && \
    /opt/yt/bin/pip install --no-cache-dir yt-dlp curl_cffi instaloader && \
    ln -s /opt/yt/bin/yt-dlp /usr/local/bin/yt-dlp && \
    ln -s /opt/yt/bin/instaloader /usr/local/bin/instaloader

# Create app directory
WORKDIR /app

# Copy Go binary
COPY --from=builder /app/downloader-bot .
RUN mkdir -p downloads

# Environment variables
ENV PORT=10000
EXPOSE 10000

# Health check
HEALTHCHECK CMD curl -f http://localhost:${PORT}/health || exit 1

# Run bot
CMD ["/app/downloader-bot"]
