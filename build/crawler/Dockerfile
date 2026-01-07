FROM golang:1.24 AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/crawler ./cmd/crawler

FROM alpine:3.18
WORKDIR /app

RUN apk add --no-cache \
    chromium \
    nss \
    freetype \
    harfbuzz \
    ca-certificates \
    ttf-freefont \
    font-noto-cjk

ENV CHROME_BIN=/usr/bin/chromium-browser

RUN addgroup -S app && adduser -S app -G app

COPY --from=builder /out/crawler /app/crawler

USER app
EXPOSE 50051
ENTRYPOINT ["/app/crawler"]
