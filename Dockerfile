# --- STAGE 1: Build the Go application ---
# Use a full Go image for compilation
FROM golang:1.22 AS builder

WORKDIR /app

# CRITICAL FIX for "go mod download" error:
# Copy the Go module files first. This allows Docker to cache the dependency
# download step if only source code changes.
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code (main.go, etc.)
COPY . .

# Build the static Go binary
# CGO_ENABLED=0 creates a static binary that runs without Glibc in the slim final image
RUN CGO_ENABLED=0 GOOS=linux go build -o /bot-app .

# --- STAGE 2: Create the final production image ---
# Use a minimal base image that is small and secure
FROM debian:bookworm-slim

# Install system dependencies: ffmpeg (for video processing) and python3/pip (for yt-dlp)
# We set the required paths for the Go application (like /usr/bin/ffmpeg)
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    ffmpeg \
    python3 \
    python3-pip && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Install yt-dlp using pip3 (required for the bot's core functionality)
RUN pip3 install yt-dlp

# Set the working directory
WORKDIR /app

# Copy the built Go binary from the builder stage
COPY --from=builder /bot-app /app/bot-app

# Configure the environment variables (PORT is used by the health check server)
ENV PORT 10000
EXPOSE 10000

# Set the default command to run your application
CMD ["/app/bot-app"]