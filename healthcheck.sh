#!/bin/sh

# 从环境变量获取SOCKS代理地址和端口
SOCKS_HOST="${USQUE_BIND_ADDRESS:-127.0.0.1}"
SOCKS_PORT="${USQUE_PORT:-1080}"

# 构建curl代理参数，处理可能的认证情况
if [ -n "$USQUE_USERNAME" ] && [ -n "$USQUE_PASSWORD" ]; then
    # 带认证的代理
    PROXY_ARGS="-x socks5h://$USQUE_USERNAME:$USQUE_PASSWORD@$SOCKS_HOST:$SOCKS_PORT"
    echo "使用带认证的SOCKS5代理进行健康检查 ($SOCKS_HOST:$SOCKS_PORT)"
else
    # 不带认证的代理
    PROXY_ARGS="-x socks5h://$SOCKS_HOST:$SOCKS_PORT"
    echo "使用不带认证的SOCKS5代理进行健康检查 ($SOCKS_HOST:$SOCKS_PORT)"
fi

# 设置curl的通用参数
CURL_OPTS="--silent --connect-timeout 5 --max-time 10 $PROXY_ARGS -o /dev/null -w %{http_code}"

# 先测试代理连通性
echo "测试SOCKS代理连通性..."
if ! curl --silent --connect-timeout 3 $PROXY_ARGS -o /dev/null http://example.com; then
    echo "警告: 无法通过SOCKS代理连接到互联网，代理可能未就绪"
    # 这里不立即退出，因为代理可能刚启动，给予后续检查的机会
fi

# 检查Google的generate_204端点
echo "检查Google连接..."
GOOGLE_HTTP_CODE=$(eval "curl $CURL_OPTS http://www.google.com/generate_204" || echo "失败")

if [ "$GOOGLE_HTTP_CODE" = "204" ]; then
    echo "健康检查成功：Google返回204状态码"
    exit 0
fi

# 如果Google检查失败，检查Cloudflare端点
echo "Google测试返回: $GOOGLE_HTTP_CODE，检查Cloudflare连接..."
CF_HTTP_CODE=$(eval "curl $CURL_OPTS http://cp.cloudflare.com/" || echo "失败")

if [ "$CF_HTTP_CODE" = "204" ]; then
    echo "健康检查成功：Cloudflare返回204状态码"
    exit 0
fi

echo "健康检查失败：Google返回 $GOOGLE_HTTP_CODE，Cloudflare返回 $CF_HTTP_CODE"
exit 1