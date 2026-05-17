#!/bin/bash
set -e

SERVICE_NAME="sentrydns"
INSTALL_DIR="/opt/sentrydns"

echo "==> Stopping services..."
systemctl stop $SERVICE_NAME 2>/dev/null || true
systemctl disable $SERVICE_NAME 2>/dev/null || true
systemctl stop sentrydps 2>/dev/null || true
systemctl disable sentrydps 2>/dev/null || true

echo "==> Removing service files..."
rm -f /etc/systemd/system/$SERVICE_NAME.service
rm -f /etc/systemd/system/sentrydps.service
systemctl daemon-reload

echo "==> Removing logrotate config..."
rm -f /etc/logrotate.d/sentrydns

echo "==> Removing binary and config..."
rm -rf "$INSTALL_DIR"

echo "==> Logs kept at /var/log/sentrydns"
echo "    Remove manually if needed: rm -rf /var/log/sentrydns"

echo ""
echo "Uninstall complete!"