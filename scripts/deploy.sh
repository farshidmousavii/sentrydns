#!/bin/bash
set -e

if [ -z "$1" ]; then
    echo "Usage: deploy.sh <user@server-ip>"
    echo "Example: deploy.sh root@192.168.1.10"
    exit 1
fi

SERVER="$1"
REMOTE_DIR="/tmp/sentrydns-deploy"
PORT="${DNS_PORT:-53}"

echo "==> Building..."
GOOS=linux GOARCH=amd64 go build -o sentrydns cmd/sentrydns/main.go

echo "==> Copying files to server..."
ssh $SERVER "mkdir -p $REMOTE_DIR/data $REMOTE_DIR/scripts"

rsync -avz sentrydns $SERVER:$REMOTE_DIR/
rsync -avz config.yaml $SERVER:$REMOTE_DIR/
rsync -avz data/iran-ranges.txt $SERVER:$REMOTE_DIR/data/
echo "==> learned.conf on server kept as-is (not overwritten)"
rsync -avz scripts/install.sh $SERVER:$REMOTE_DIR/scripts/
rsync -avz scripts/sentrydps.sh $SERVER:$REMOTE_DIR/scripts/
rsync -avz scripts/sentrydps.service $SERVER:$REMOTE_DIR/scripts/
rsync -avz scripts/logrotate.conf $SERVER:$REMOTE_DIR/scripts/

echo "==> Backing up current deployment on server..."
BACKUP_DIR="/opt/sentrydns/backup"
ssh -t $SERVER "sudo mkdir -p $BACKUP_DIR && \
	sudo cp /opt/sentrydns/sentrydns $BACKUP_DIR/sentrydns.bak 2>/dev/null; \
	sudo cp /opt/sentrydns/config.yaml $BACKUP_DIR/config.yaml.bak 2>/dev/null; \
	sudo cp /opt/sentrydns/data/learned.conf $BACKUP_DIR/learned.conf.bak 2>/dev/null; \
	true"

echo "==> Running install on server..."
ssh -t $SERVER "cd $REMOTE_DIR && sudo bash scripts/install.sh"

echo "==> Restarting services..."
ssh -t $SERVER "sudo systemctl restart sentrydns && sudo systemctl restart sentrydps"

echo "==> Verifying..."
sleep 2

ROLLBACK=0
ssh -t $SERVER "sudo systemctl is-active --quiet sentrydns" \
    && echo "OK: sentrydns is running" \
    || { echo "ERROR: sentrydns not active"; ROLLBACK=1; }

if [ "$ROLLBACK" -eq 0 ]; then
    if ssh $SERVER "command -v dig >/dev/null 2>&1"; then
        if ssh $SERVER "dig +short @127.0.0.1 -p $PORT google.com 2>/dev/null | grep -q '^[0-9]'"; then
            echo "OK: sentrydns is responding to queries"
        else
            echo "WARNING: sentrydns not responding on port $PORT"
        fi
    fi
fi

if [ "$ROLLBACK" -eq 1 ]; then
    echo "==> ROLLING BACK..."
    ssh -t $SERVER "sudo systemctl stop sentrydns && \
        sudo cp $BACKUP_DIR/sentrydns.bak /opt/sentrydns/sentrydns && \
        sudo chmod +x /opt/sentrydns/sentrydns && \
        sudo systemctl start sentrydns" || true
    sleep 2
    ssh -t $SERVER "sudo systemctl is-active --quiet sentrydns" \
        && echo "OK: rollback succeeded" \
        || echo "CRITICAL: rollback also failed — manual intervention required"
    ssh -t $SERVER "sudo journalctl -u sentrydns -n 30 --no-pager"
    ssh -t $SERVER "sudo rm -rf $REMOTE_DIR"
    exit 1
fi

ssh -t $SERVER "sudo systemctl is-active --quiet sentrydps" \
    && echo "OK: sentrydps is running" \
    || echo "WARNING: sentrydps not active — check 'systemctl status sentrydps'"

if ssh $SERVER "command -v dig >/dev/null 2>&1"; then
    if ssh $SERVER "dig +short @127.0.0.1 -p $PORT google.com 2>/dev/null | grep -q '^[0-9]'"; then
        echo "OK: sentrydns is responding to queries"
    else
        echo "WARNING: sentrydns not responding on port $PORT"
    fi
fi

echo "==> Cleaning up..."
ssh -t $SERVER "sudo rm -rf $REMOTE_DIR"

echo ""
echo "Deploy complete!"