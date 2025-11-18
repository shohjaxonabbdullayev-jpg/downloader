# ============================
# 🏗️ STAGE 1 — Build Go binary
# ============================
FROM golang:1.24.4 AS builder

# Set working directory
WORKDIR /app

# Install build dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends build-essential && \
    rm -rf /var/lib/apt/lists/*

# Copy Go modules and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
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
        python3 \
        python3-venv \
        python3-pip \
        ca-certificates \
        curl \
        wget \
        git && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Create Python virtual environment and install packages
RUN python3 -m venv /opt/yt && \
    /opt/yt/bin/pip install --upgrade pip && \
    /opt/yt/bin/pip install --no-cache-dir yt-dlp gallery-dl && \
    ln -s /opt/yt/bin/yt-dlp /usr/local/bin/yt-dlp && \
    ln -s /opt/yt/bin/gallery-dl /usr/local/bin/gallery-dl

# Set working directory
WORKDIR /app

# Copy Go binary from builder stage
COPY --from=builder /app/downloader-bot .

# Copy credential/config files
COPY twitter.txt facebook.txt instagram.txt youtube.txt pinterest.txt ./

# Create downloads directory
RUN mkdir -p downloads

# Set environment variables
ENV PORT=10000

# Expose port
EXPOSE 10000

# Healthcheck
HEALTHCHECK CMD curl -f http://localhost:${PORT}/health || exit 1

# Run the Go bot
CMD ["/app/downloader-bot"]
