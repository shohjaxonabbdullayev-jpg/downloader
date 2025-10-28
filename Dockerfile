# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS builder

# Install git + ca-certificates so go mod can fetch HTTPS modules
RUN apk add --no-cache git ca-certificates && update-ca-certificates

WORKDIR /app

# Let Go download correct toolchain automatically
ENV GOTOOLCHAIN=go1.24.4
ENV GOPROXY=https://proxy.golang.org,direct
ENV GOSUMDB=sum.golang.org

# Copy dependency files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build app
RUN go build -o main .

# Final minimal image
FROM alpine:latest
RUN apk add --no-cache ca-certificates && update-ca-certificates
WORKDIR /app
COPY --from=builder /app/main .

CMD ["./main"]
