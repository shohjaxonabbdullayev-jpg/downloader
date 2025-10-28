# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS builder

# Install git (needed for go mod download)
RUN apk add --no-cache git

WORKDIR /app

# Copy dependency files first for caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the app
COPY . .

# Build the Go binary
RUN go build -o main .

# Minimal runtime image
FROM alpine:latest

WORKDIR /app
COPY --from=builder /app/main .

CMD ["./main"]
