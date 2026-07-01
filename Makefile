BIN_DIR := $(shell pwd)/bin
HOOK    := $(BIN_DIR)/agent-monitor-hook
SETUP   := $(BIN_DIR)/agent-monitor-setup

.PHONY: build
build:
	@mkdir -p $(BIN_DIR)
	go build -o $(HOOK)  ./cmd/hook
	go build -o $(SETUP) ./cmd/setup
	@echo "✓ hook + setup built → $(BIN_DIR)/"

.PHONY: test
test:
	go test ./internal/... ./sdk/... -count=1

.PHONY: install
install: build
	@install -m 755 $(HOOK)  /usr/local/bin/agent-monitor-hook
	@install -m 755 $(SETUP) /usr/local/bin/agent-monitor-setup
	@echo "✓ installed to /usr/local/bin/"

.PHONY: clean
clean:
	@rm -rf $(BIN_DIR)
