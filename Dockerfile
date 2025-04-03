FROM golang:alpine AS builder

WORKDIR /app

COPY go.mod .
COPY go.sum .

RUN go mod download

COPY . .

RUN go build -o usque -ldflags="-s -w" .

# scratch won't be enough, because we need a cert store
FROM alpine:latest

WORKDIR /app

# Create etc directory for configuration
RUN mkdir -p /app/etc

COPY --from=builder /app/usque /bin/usque
# Copy the entrypoint script from the build context
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

ENTRYPOINT ["/app/entrypoint.sh"]