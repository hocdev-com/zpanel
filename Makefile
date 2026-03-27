# Makefile for zPanel cross-platform build

BINARY_NAME=zpanel
CMD_DIR=./cmd/zpanel
BUILD_DIR=./build

.PHONY: all clean windows linux

all: windows linux

windows:
	@echo "Building for Windows..."
	mkdir -p $(BUILD_DIR)/windows
	GOOS=windows GOARCH=amd64 go build -o $(BUILD_DIR)/windows/$(BINARY_NAME).exe $(CMD_DIR)

linux:
	@echo "Building for Linux (VPS)..."
	mkdir -p $(BUILD_DIR)/linux
	GOOS=linux GOARCH=amd64 go build -o $(BUILD_DIR)/linux/$(BINARY_NAME) $(CMD_DIR)

clean:
	rm -rf $(BUILD_DIR)

help:
	@echo "Usage:"
	@echo "  make windows   Build for Windows x64"
	@echo "  make linux     Build for Linux x64 (VPS)"
	@echo "  make all       Build for both platforms"
	@echo "  make clean     Remove build directory"
