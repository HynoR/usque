FROM golang:alpine AS builder
WORKDIR /app
COPY go.mod .
COPY go.sum .
RUN go mod download
COPY . .
RUN go build -o usque -ldflags="-s -w" .

FROM alpine:latest
WORKDIR /app
# 复制 entrypoint.sh 到容器中
COPY entrypoint.sh /app/
RUN chmod +x /app/entrypoint.sh
COPY --from=builder /app/usque /bin/usque
# 使用 entrypoint.sh 作为入口点
ENTRYPOINT ["/app/entrypoint.sh"]