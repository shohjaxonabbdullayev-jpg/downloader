# ===================== Base image =====================
FROM golang:1.24-bookworm

# ===================== Install dependencies =====================
RUN apt-get update && apt-get install -y \
    ffmpeg \
    curl \
    python3-pip \
    && curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp \
    && chmod a+rx /usr/local/bin/yt-dlp \
    && python3 -m pip install --upgrade pip

# ===================== Set working directory =====================
WORKDIR /app

# ===================== Copy Go modules and download dependencies =====================
COPY go.mod go.sum ./
RUN go mod download

# ===================== Copy the app and cookies.txt =====================
COPY . .

# ===================== Ensure downloads directory exists =====================
RUN mkdir -p downloads

# ===================== Build the Go app =====================
RUN go build -o main .

# ===================== Expose port =====================
EXPOSE 10000

# ===================== Run app =====================
CMD ["./main"]
