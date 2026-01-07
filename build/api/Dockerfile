FROM golang:1.24 AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/api ./cmd/api

FROM alpine:3.18
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata
ENV TZ=Asia/Tokyo

COPY --from=builder /out/api /app/api
COPY web /app/web

EXPOSE 8080
ENTRYPOINT ["/app/api"]
