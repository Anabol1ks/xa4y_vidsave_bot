FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bot ./cmd/bot

# --- runtime ---
FROM alpine:3.21

ARG YT_DLP_VERSION=2026.10.26
ARG GALLERY_DL_VERSION=1.32.19

RUN apk add --no-cache ca-certificates python3 py3-pip ffmpeg nodejs \
    && pip3 install --break-system-packages --no-cache-dir "yt-dlp[default]==${YT_DLP_VERSION}" "gallery-dl==${GALLERY_DL_VERSION}" \
    && node --version && yt-dlp --version && gallery-dl --version

COPY --from=builder /bot /bot

ENTRYPOINT ["/bot"]
