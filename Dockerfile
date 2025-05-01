FROM golang:alpine AS builder
WORKDIR /app
COPY go.mod .
COPY go.sum .
RUN go mod download
COPY . .
RUN go build -o usque -ldflags="-s -w" .

FROM alpine:latest
WORKDIR /app
# 安装curl工具
RUN apk add --no-cache curl
# 复制脚本到容器中
COPY entrypoint.sh /app/
COPY healthcheck.sh /app/
RUN chmod +x /app/entrypoint.sh /app/healthcheck.sh
COPY --from=builder /app/usque /bin/usque
# 配置健康检查
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 CMD ["/app/healthcheck.sh"]
# 使用entrypoint.sh作为入口点
ENTRYPOINT ["/app/entrypoint.sh"]