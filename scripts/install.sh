#!/bin/bash
set -e

INSTALL_DIR="/opt/sentrydns"
SERVICE_NAME="sentrydns"
BINARY="./sentrydns"
CONFIG="./config.yaml"

echo "==> Creating directories..."
mkdir -p "$INSTALL_DIR/data"
	mkdir -p "/var/log/sentrydns"

echo "==> Stopping service..."
systemctl stop $SERVICE_NAME 2>/dev/null || true
for i in $(seq 1 10); do
    if ! systemctl is-active --quiet $SERVICE_NAME 2>/dev/null; then
        break
    fi
    sleep 0.5
done

echo "==> Copying files..."
cp "$BINARY" "$INSTALL_DIR/sentrydns"
chmod +x "$INSTALL_DIR/sentrydns"

cp "$CONFIG" "$INSTALL_DIR/config.yaml"
echo "==> config.yaml copied"

cp data/iran-ranges.txt "$INSTALL_DIR/data/"
if [ ! -f "$INSTALL_DIR/data/learned.conf" ]; then
    cp data/learned.conf "$INSTALL_DIR/data/"
    echo "==> learned.conf copied"
else
    sort -u "$INSTALL_DIR/data/learned.conf" data/learned.conf -o "$INSTALL_DIR/data/learned.conf"
    echo "==> learned.conf merged with local"
fi

echo "==> Installing systemd service..."
cat > /etc/systemd/system/$SERVICE_NAME.service << EOF
[Unit]
Description=SentryDNS - Intelligent Iran/Global DNS Routing
After=network.target
Wants=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/sentrydns
ExecStart=/opt/sentrydns/sentrydns -config /opt/sentrydns/config.yaml
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

echo "==> Reloading systemd..."
systemctl daemon-reload
systemctl enable $SERVICE_NAME

echo ""
echo "Installation complete!"
echo ""
echo "Commands:"
echo "  Start:   systemctl start $SERVICE_NAME"
echo "  Stop:    systemctl stop $SERVICE_NAME"
echo "  Status:  systemctl status $SERVICE_NAME"
echo "  Logs:    tail -f /var/log/sentrydns/sentrydns.log"