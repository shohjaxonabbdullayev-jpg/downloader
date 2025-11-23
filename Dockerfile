# =========================
# 🏗️ BUILD STAGE
# =========================
FROM golang:1.24.4 AS builder

WORKDIR /app

# Install dependencies
RUN apt-get update && apt-get install -y --no-install-recommends build-essential python3 python3-pip && \
    rm -rf /var/lib/apt/lists/*

# Download Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build Go binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o downloader-bot .

# =========================
# 🚀 FINAL STAGE
# =========================
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    ffmpeg python3 python3-pip ca-certificates curl wget git && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# Install Python tools
RUN python3 -m pip install --no-cache-dir yt-dlp gallery-dl

WORKDIR /app
COPY --from=builder /app/downloader-bot .

# Create download folder
RUN mkdir -p downloads

# Port & Health
ENV PORT=8080
EXPOSE 8080
HEALTHCHECK CMD curl -f http://localhost:${PORT}/health || exit 1

CMD ["/app/downloader-bot"]
