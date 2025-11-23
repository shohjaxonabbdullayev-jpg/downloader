# =========================
# Stage 1: Build Go binary
# =========================
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git bash ca-certificates build-base

WORKDIR /app

# Copy go.mod and go.sum first (for caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build Go binary (statically linked)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o downloader-bot .

# =========================
# Stage 2: Create minimal image
# =========================
FROM alpine:3.18

# Install runtime dependencies
RUN apk add --no-cache bash ca-certificates ffmpeg python3 py3-pip yt-dlp gallery-dl

WORKDIR /app

# Copy Go binary from builder
COPY --from=builder /app/downloader-bot .

# Copy any other assets if needed (optional)
# COPY ./downloads ./downloads

# Expose port if needed (for webhooks/health checks)
EXPOSE 8080

# Start the bot
CMD ["./downloader-bot"]
