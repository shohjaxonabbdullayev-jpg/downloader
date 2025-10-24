# Use official Go image
FROM golang:1.22-bullseye

# Install ffmpeg and yt-dlp
RUN apt-get update && \
    apt-get install -y ffmpeg python3 python3-pip && \
    pip3 install -U yt-dlp

# Set working directory
WORKDIR /app

# Copy all project files
COPY . .

# Build your Go app
RUN go build -o bot main.go

# Set environment variable for port
ENV PORT=10000

# Expose port for health check
EXPOSE 10000

# Run bot
CMD ["./bot"]
