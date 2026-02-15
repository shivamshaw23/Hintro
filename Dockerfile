# ── Build stage ───────────────────────────────────────
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Copy everything and resolve dependencies.
COPY . .
RUN go mod tidy
RUN go mod download

# Build the binary.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /bin/server ./cmd/server

# ── Runtime stage ────────────────────────────────────
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata postgresql16-client

COPY --from=builder /bin/server /usr/local/bin/server

# Copy migrations and entrypoint into the image.
COPY migrations/ /app/migrations/
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

EXPOSE 8080

ENTRYPOINT ["/app/entrypoint.sh"]
