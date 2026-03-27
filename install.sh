#!/bin/bash

# aaPanel Lite - VPS Installer (Ubuntu/CentOS)
# Support: Ubuntu 20.04+, CentOS 7+

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

echo -e "${GREEN}Starting aaPanel Lite Installation...${NC}"

# Check for root
if [ "$EUID" -ne 0 ]; then
  echo -e "${RED}Please run as root (sudo bash install.sh)${NC}"
  exit 1
fi

# Detect OS
if [ -f /etc/os-release ]; then
    . /etc/os-release
    OS=$ID
else
    echo -e "${RED}Unknown OS. Assuming Ubuntu...${NC}"
    OS="ubuntu"
fi

# Install Dependencies
echo -e "Installing dependencies for ${OS}..."
if [ "$OS" == "ubuntu" ] || [ "$OS" == "debian" ]; then
    apt-get update
    apt-get install -y wget curl tar gzip
elif [ "$OS" == "centos" ] || [ "$OS" == "rhel" ]; then
    yum install -y wget curl tar gzip
fi

# Create directory
INSTALL_DIR="/opt/zpanel"
mkdir -p $INSTALL_DIR
cd $INSTALL_DIR

# Download Binary (Placeholder URL - replace with your actual release URL)
# For now, we assume the binary 'zpanel' is in the same directory as this script
if [ -f "./zpanel" ]; then
    echo "Using local binary."
else
    echo -e "${RED}Binary 'zpanel' not found in current directory.${NC}"
    echo "Please build it using 'make linux' first and place it here."
    exit 1
fi

chmod +x zpanel

# Create Systemd Service
echo "Creating systemd service..."
cat > /etc/systemd/system/zpanel.service <<EOF
[Unit]
Description=aaPanel Lite Control Panel
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/zpanel -port 8888
Restart=always

[Install]
WantedBy=multi-user.target
EOF

# Start Service
systemctl daemon-reload
systemctl enable zpanel
systemctl start zpanel

echo -e "${GREEN}aaPanel Lite installed successfully!${NC}"
echo -e "Access URL: http://$(curl -s ifconfig.me):8888"
echo -e "Management: systemctl [start|stop|status] zpanel"
