#!/bin/sh

# 定义一个函数来运行 usque socks 并实现进程守护
run_socks_with_monitoring() {
    echo "启动 usque socks 并实现进程守护..."
    while true; do
        /bin/usque socks
        echo "usque socks 进程退出，5秒后重新启动..."
        sleep 5
    done
}

# 检查 config.json 是否存在
if [ ! -f "config.json" ]; then
    echo "未找到 config.json，运行 usque register 并自动接受服务条款..."
    # 使用 echo 命令自动回答 "y" 来接受服务条款
    echo "y" | /bin/usque register
    
    # 检查 register 命令执行后是否创建了 config.json
    if [ -f "config.json" ]; then
        echo "注册成功，已创建 config.json。"
        run_socks_with_monitoring
    else
        echo "错误：注册后未能创建 config.json。"
        exit 1
    fi
else
    echo "已找到 config.json，运行 usque socks..."
    run_socks_with_monitoring
fi