# --- STAGE 1: Build the Go application ---
FROM golang:1.24.4 AS builder

WORKDIR /app

# Install build tools (needed for CGO and linking)
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# Copy Go module files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the Go binary
RUN GOOS=linux go build -o /bot-app .

# --- STAGE 2: Create the final image ---
FROM debian:bookworm-slim

# Install ffmpeg + Python + pip (for yt-dlp)
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    ffmpeg \
    python3 \
    python3-pip \
    ca-certificates \
    && apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Install yt-dlp
RUN pip3 install yt-dlp

WORKDIR /app

# Copy the built Go binary
COPY --from=builder /bot-app /app/bot-app

# Environment setup
ENV PORT=10000
EXPOSE 10000

# Run the app
CMD ["/app/bot-app"]
