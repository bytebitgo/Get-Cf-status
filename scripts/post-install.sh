#!/bin/bash

# 复制 systemd 服务文件
cp /etc/cf-status/cf-status.service /etc/systemd/system/

# 重新加载 systemd
systemctl daemon-reload

# 启用并启动服务
systemctl enable cf-status
systemctl start cf-status

echo "Cloudflare Status Monitor has been installed and started." 