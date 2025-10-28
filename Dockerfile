# Use a multi-stage build for a smaller final image
# --- STAGE 1: Build the Go application ---
FROM golang:1.22 AS builder

WORKDIR /app

# Copy the Go module files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the Go binary
RUN CGO_ENABLED=0 GOOS=linux go build -o /bot-app .

# --- STAGE 2: Create the final production image ---
# Use a slim Linux distribution to keep the final image small
FROM debian:bookworm-slim

# Install system dependencies: ffmpeg and python3 (for yt-dlp)
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    ffmpeg \
    python3 \
    python3-pip && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Install yt-dlp using pip
RUN pip3 install yt-dlp

# Set the working directory
WORKDIR /app

# Copy the built Go binary from the builder stage
COPY --from=builder /bot-app /app/bot-app

# The constant ffmpegPath = "/usr/bin" in main.go points to where ffmpeg is installed

# Expose the port (Render's internal network needs this)
ENV PORT 10000
EXPOSE 10000

# Set the default command to run your application
CMD ["/app/bot-app"]
