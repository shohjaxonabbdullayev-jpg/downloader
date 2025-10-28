# --- STAGE 1: Build the Go application ---
# Use a full Go image for compilation
FROM golang:1.22 AS builder

WORKDIR /app

# CRITICAL FIX 1: Install C dependencies needed by CGO for certain Go libraries.
# This prevents linker errors during the build stage.
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# CRITICAL FIX 2: Copy the Go module files first.
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code (main.go, etc.)
COPY . .

# Build the Go binary (removing CGO_ENABLED=0 to support telegram-bot-api)
RUN GOOS=linux go build -o /bot-app .

# --- STAGE 2: Create the final production image ---
# Use a minimal base image that is small and secure
FROM debian:bookworm-slim

# Install system dependencies: ffmpeg (for video processing) and python3/pip (for yt-dlp)
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    ffmpeg \
    python3 \
    python3-pip \
    ca-certificates \
    && apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Install yt-dlp using pip3 (required for the bot's core functionality)
RUN pip3 install yt-dlp

# Set the working directory
WORKDIR /app

# Copy the built Go binary from the builder stage
# IMPORTANT: Only copy the essential binary, not placeholder files.
COPY --from=builder /bot-app /app/bot-app

# Configure the environment variables
ENV PORT 10000
EXPOSE 10000

# Set the default command to run your application
CMD ["/app/bot-app"]