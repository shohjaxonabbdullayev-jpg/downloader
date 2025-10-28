# ---- Builder Stage ----
FROM golang:1.24.4-alpine AS builder

# Set environment variables
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Set working directory
WORKDIR /app

# Copy go.mod and go.sum first for caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the Go binary
RUN go build -o main .

# ---- Final Runtime Stage ----
FROM alpine:3.19

# Install runtime dependencies (yt-dlp, ffmpeg, python3, etc.)
RUN apk add --no-cache ffmpeg python3 py3-pip py3-wheel py3-setuptools yt-dlp ca-certificates \
    && update-ca-certificates

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/main .

# Copy .env if you want (Render usually injects env vars automatically)
# COPY .env .env

# Expose port for health check (optional)
EXPOSE 8080

# Run the app
CMD ["./main"]
