#!/bin/sh

echo "DO COMMAND BELOW OR WRITE SYSCTL CONFIG TO IMPROVE UDP NETWORK"
echo "sysctl -w net.core.rmem_max=7500000"
echo "sysctl -w net.core.wmem_max=7500000"


if [ -f /app/etc/config.json ]; then
  echo "Using existing config.json"
  exec /bin/usque socks -c /app/etc/config.json
else
  echo "No config.json found, running registration..."
  if /bin/usque register --tos; then
    echo "Registration successful, moving config.json to /app/etc/"
    mv config.json /app/etc/
    exec /bin/usque socks -c /app/etc/config.json
  else
    echo "Registration failed, exiting"
    exit 1
  fi
fi