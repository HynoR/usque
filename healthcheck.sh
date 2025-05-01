#!/bin/sh

# 检查Google的generate_204端点
echo "检查Google连接..."
GOOGLE_HTTP_CODE=$(curl --silent --connect-timeout 5 --max-time 10 -x socks5://127.0.0.1:1080 -o /dev/null -w "%{http_code}" http://www.google.com/generate_204 || echo "失败")

if [ "$GOOGLE_HTTP_CODE" = "204" ]; then
    echo "健康检查成功：Google返回204状态码"
    exit 0
fi

# 如果Google检查失败，检查Cloudflare端点
echo "Google测试返回: $GOOGLE_HTTP_CODE，检查Cloudflare连接..."
CF_HTTP_CODE=$(curl --silent --connect-timeout 5 --max-time 10 -x socks5://127.0.0.1:1080 -o /dev/null -w "%{http_code}" http://cp.cloudflare.com/ || echo "失败")

if [ "$CF_HTTP_CODE" = "204" ]; then
    echo "健康检查成功：Cloudflare返回204状态码"
    exit 0
fi

echo "健康检查失败：Google返回 $GOOGLE_HTTP_CODE，Cloudflare返回 $CF_HTTP_CODE"
exit 1