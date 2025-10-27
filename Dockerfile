# Base image: Go
FROM golang:1.24-bullseye

# Install Python 3.10, ffmpeg, curl
RUN apt-get update && apt-get install -y \
    software-properties-common \
    && add-apt-repository ppa:deadsnakes/ppa \
    && apt-get update && apt-get install -y \
    python3.10 python3.10-venv python3.10-dev python3-pip \
    ffmpeg curl \
    && curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp \
    && chmod a+rx /usr/local/bin/yt-dlp \
    && python3.10 -m pip install --upgrade pip

# Set working directory
WORKDIR /app

# Copy Go modules first (caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy the app and cookies.txt
COPY . .

# Ensure downloads directory exists
RUN mkdir -p downloads

# Build the Go app
RUN go build -o main .

EXPOSE 10000
CMD ["./main"]
