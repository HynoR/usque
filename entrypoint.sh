#!/bin/sh

# 构建usque socks命令的函数
build_socks_command() {
    SOCKS_CMD="/bin/usque socks -b $USQUE_BIND_ADDRESS -p $USQUE_PORT"
    
    # 如果提供了用户名和密码，添加认证参数
    if [ -n "$USQUE_USERNAME" ] && [ -n "$USQUE_PASSWORD" ]; then
        SOCKS_CMD="$SOCKS_CMD -u $USQUE_USERNAME -w $USQUE_PASSWORD"
        echo "启用SOCKS5代理认证"
    else
        echo "SOCKS5代理未启用认证"
    fi
    
    echo "使用命令: $SOCKS_CMD (绑定: $USQUE_BIND_ADDRESS:$USQUE_PORT)"
    eval $SOCKS_CMD
}

# 定义一个函数来运行usque socks并实现进程守护
run_socks_with_monitoring() {
    echo "启动usque socks并实现进程守护..."
    while true; do
        build_socks_command
        echo "usque socks进程退出，5秒后重新启动..."
        sleep 5
    done
}

# 检查config.json是否存在
if [ ! -f "config.json" ]; then
    echo "未找到config.json，运行usque register并自动接受服务条款..."
    # 使用echo命令自动回答"y"来接受服务条款
    echo "y" | /bin/usque register
    
    # 检查register命令执行后是否创建了config.json
    if [ -f "config.json" ]; then
        echo "注册成功，已创建config.json。"
        run_socks_with_monitoring
    else
        echo "错误：注册后未能创建config.json。"
        exit 1
    fi
else
    echo "已找到config.json，运行usque socks..."
    run_socks_with_monitoring
fi