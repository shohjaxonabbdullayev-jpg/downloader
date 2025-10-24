# Use an official lightweight Debian image
FROM golang:1.22-bullseye

# Install dependencies
RUN apt-get update && apt-get install -y \
    python3 \
    python3-pip \
    ffmpeg \
 && pip3 install -U yt-dlp \
 && rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /app

# Copy Go files
COPY . .

# Build your app
RUN go build -o app .

# Expose the port (matches your Go health check)
EXPOSE 10000

# Start your bot
CMD ["./app"]
