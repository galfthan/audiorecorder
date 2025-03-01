# Makefile for Windows Audio Recorder
# Cross-compilation from WSL to Windows

# Configuration
APP_NAME := AudioRecorder
OUT_DIR := dist
MAIN_GO := main.go
GO := go
WINDRES := x86_64-w64-mingw32-windres

# Cross-compilation settings
GOOS := windows
GOARCH := amd64
CGO_ENABLED := 1
CC := x86_64-w64-mingw32-gcc
CXX := x86_64-w64-mingw32-g++

# Default target
.PHONY: all
all: deps ensure_out_dir compile

# Create output directory
.PHONY: ensure_out_dir
ensure_out_dir:
	mkdir -p $(OUT_DIR)

# Install dependencies
.PHONY: deps
deps:
	$(GO) mod tidy
	$(GO) mod download

# Build the main executable
.PHONY: compile
compile: deps ensure_out_dir
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=$(CGO_ENABLED) CC=$(CC) CXX=$(CXX) \
	$(GO) build -ldflags="-H windowsgui" -o $(OUT_DIR)/$(APP_NAME).exe
	@echo "Build complete: $(OUT_DIR)/$(APP_NAME).exe"

# Alias for backward compatibility
.PHONY: build
build: compile

# Build with icon resource (if available)
.PHONY: compile_with_icon
compile_with_icon: deps ensure_out_dir icon.syso
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=$(CGO_ENABLED) CC=$(CC) CXX=$(CXX) \
	$(GO) build -ldflags="-H windowsgui" -o $(OUT_DIR)/$(APP_NAME).exe
	@echo "Build with icon complete: $(OUT_DIR)/$(APP_NAME).exe"

# Compile icon resource file
icon.syso: icon.rc
	$(WINDRES) icon.rc -O coff -o icon.syso

# Create resource file (requires icon.ico)
icon.rc:
	@if [ -f icon.ico ]; then \
		echo "Creating resource file..."; \
		echo "id ICON \"icon.ico\"" > icon.rc; \
	else \
		echo "Warning: icon.ico not found, skipping resource compilation"; \
		touch icon.rc; \
	fi

# List required build tools
.PHONY: requirements
requirements:
	@echo "Required build tools:"
	@echo "- golang"
	@echo "- gcc-mingw-w64"
	@echo "- libgl1-mesa-dev"
	@echo "- xorg-dev"

# Initialize a new Go module
.PHONY: init
init:
	$(GO) mod init audio_recorder
	touch $(MAIN_GO)

# Clean build artifacts
.PHONY: clean
clean:
	rm -rf $(OUT_DIR)
	rm -f icon.syso icon.rc

# Run tests
.PHONY: test
test:
	$(GO) test ./...

# Help target
.PHONY: help
help:
	@echo "Windows Audio Recorder Makefile"
	@echo ""
	@echo "Available targets:"
	@echo "  all              - Install dependencies and build executable"
	@echo "  build            - Alias for 'compile'"
	@echo "  compile          - Build the Windows executable"
	@echo "  compile_with_icon - Build with custom icon (requires icon.ico)"
	@echo "  clean            - Remove build artifacts"
	@echo "  deps             - Install Go dependencies"
	@echo "  help             - Display this help message"
	@echo "  init             - Initialize a new Go module"
	@echo "  requirements     - List required build tools"
	@echo "  test             - Run tests"