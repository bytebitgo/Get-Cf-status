#!/bin/bash

# 停止并禁用服务
systemctl stop cf-status
systemctl disable cf-status

# 删除 systemd 服务文件
rm -f /etc/systemd/system/cf-status.service

# 重新加载 systemd
systemctl daemon-reload

echo "Cloudflare Status Monitor has been stopped and disabled." 