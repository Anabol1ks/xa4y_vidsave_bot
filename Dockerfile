FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bot ./cmd/bot

# --- runtime ---
FROM alpine:3.21

ARG YT_DLP_VERSION=2026.7.4

RUN apk add --no-cache ca-certificates python3 py3-pip ffmpeg \
    && pip3 install --break-system-packages --no-cache-dir "yt-dlp==${YT_DLP_VERSION}" \
    && yt-dlp --version

COPY --from=builder /bot /bot

ENTRYPOINT ["/bot"]
