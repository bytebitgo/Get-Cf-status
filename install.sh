#!/bin/bash

# 编译程序
go build -o cf-status

# 创建必要的目录
sudo mkdir -p /etc/cf-status
sudo mkdir -p /usr/local/bin

# 复制文件
sudo cp cf-status /usr/local/bin/
sudo cp env.config /etc/cf-status/
sudo cp cf-status.service /etc/systemd/system/

# 设置权限
sudo chown nobody:nobody /etc/cf-status/env.config
sudo chmod 600 /etc/cf-status/env.config
sudo chmod 755 /usr/local/bin/cf-status

# 重新加载 systemd
sudo systemctl daemon-reload

echo "安装完成！"
echo "请编辑 /etc/cf-status/env.config 配置文件，然后运行以下命令启动服务："
echo "sudo systemctl start cf-status"
echo "sudo systemctl enable cf-status  # 设置开机自启" 