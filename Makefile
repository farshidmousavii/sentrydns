BINARY=sentrydns
BUILD_DIR=.
INSTALL_DIR=/opt/sentrydns
SERVER?=user@server-ip

.PHONY: build install uninstall start stop status logs clean deploy deploy-binary \
        download-ranges download-domains test test-race cover cover-html lint

build:
	GOOS=linux GOARCH=amd64 go build -o $(BINARY) cmd/sentrydns/main.go

install: build
	sudo bash scripts/install.sh
	sudo cp scripts/logrotate.conf /etc/logrotate.d/sentrydns

uninstall:
	sudo bash scripts/uninstall.sh

start:
	sudo systemctl start $(BINARY)

stop:
	sudo systemctl stop $(BINARY)

status:
	sudo systemctl status $(BINARY)

logs:
	sudo tail -f /var/log/sentrydns/sentrydns.log

download-ranges:
	curl -o data/iran-ranges.txt https://raw.githubusercontent.com/farshidmousavii/iran-ip/main/ipv4.txt

download-domains:
	curl -o data/learned.conf https://github.com/bootmortis/iran-hosted-domains/releases/download/202605110145/domains.txt

test:
	go test ./...

test-race:
	go test -race ./...

cover:
	go test -coverprofile=coverage.out -covermode=atomic ./...
	@echo "---"
	@go tool cover -func=coverage.out | tail -20

cover-html: cover
	go tool cover -html=coverage.out -o coverage.html

lint:
	go vet ./...

clean:
	rm -f $(BINARY) coverage.out coverage.html

deploy: build
	bash scripts/deploy.sh $(SERVER)

deploy-binary: build
	scp $(BINARY) $(SERVER):/tmp/
	ssh -t $(SERVER) "sudo systemctl stop $(BINARY) && \
		sudo cp /tmp/$(BINARY) $(INSTALL_DIR)/$(BINARY) && \
		sudo chmod +x $(INSTALL_DIR)/$(BINARY) && \
		sudo systemctl start $(BINARY) && \
		sudo systemctl status $(BINARY)"