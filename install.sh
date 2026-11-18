#!/bin/bash
set -e

# bbkt installer
# Usage: curl -sSL https://raw.githubusercontent.com/zach-snell/bbkt/main/install.sh | bash

REPO="zach-snell/bbkt"
BINARY_NAME="bbkt"

# Default install dir, can be overridden with --user or INSTALL_DIR env var
if [ "$1" = "--user" ]; then
    INSTALL_DIR="${HOME}/.local/bin"
    mkdir -p "$INSTALL_DIR"
else
    INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
fi

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# printf, not echo -e: under sh (e.g. "curl ... | sh") echo prints a literal -e.
info() { printf '%b==>%b %s\n' "$BLUE" "$NC" "$1"; }
success() { printf '%b==>%b %s\n' "$GREEN" "$NC" "$1"; }
warn() { printf '%b==>%b %s\n' "$YELLOW" "$NC" "$1"; }
error() { printf '%b==>%b %s\n' "$RED" "$NC" "$1" >&2; exit 1; }

# Detect OS
detect_os() {
    case "$(uname -s)" in
        Linux*)  echo "linux" ;;
        Darwin*) echo "darwin" ;;
        MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
        *) error "Unsupported OS: $(uname -s)" ;;
    esac
}

# Detect architecture
detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64) echo "amd64" ;;
        arm64|aarch64) echo "arm64" ;;
        *) error "Unsupported architecture: $(uname -m)" ;;
    esac
}

# Get latest version
get_latest_version() {
    curl -sSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/'
}

# Check if command exists
has_command() {
    command -v "$1" >/dev/null 2>&1
}

# Compute SHA-256 of a file (sha256sum on Linux, shasum on macOS)
sha256_of() {
    if has_command sha256sum; then
        sha256sum "$1" | awk '{print $1}'
    elif has_command shasum; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        error "sha256sum or shasum is required to verify the download"
    fi
}

# Verify FILE against the entry for NAME in the checksums file SUMS.
# Fails closed: a missing entry or a mismatch aborts the install.
verify_checksum() {
    local file="$1" name="$2" sums="$3" expected actual
    expected=$(awk -v n="$name" '{f=$2; sub(/^\*/, "", f); if (f == n) {print $1; exit}}' "$sums")
    if [ -z "$expected" ]; then
        error "No checksum entry for ${name} in checksums.txt; refusing to install"
    fi
    actual=$(sha256_of "$file")
    if [ "$actual" != "$expected" ]; then
        error "Checksum mismatch for ${name} (expected ${expected}, got ${actual}); refusing to install"
    fi
}

# Main install
main() {
    echo ""
    printf '%b%s%b\n' "$GREEN" "╔═══════════════════════════════════════╗" "$NC"
    printf '%b%s%b\n' "$GREEN" "║     bbkt installer                    ║" "$NC"
    printf '%b%s%b\n' "$GREEN" "╚═══════════════════════════════════════╝" "$NC"
    echo ""

    # Check for curl
    if ! has_command curl; then
        error "curl is required but not installed"
    fi

    if ! has_command sha256sum && ! has_command shasum; then
        error "sha256sum or shasum is required to verify the download"
    fi

    OS=$(detect_os)
    ARCH=$(detect_arch)
    VERSION=$(get_latest_version)

    if [ -z "$VERSION" ]; then
        error "Failed to get latest version"
    fi

    info "Detected: ${OS}/${ARCH}"
    info "Latest version: ${VERSION}"

    # Build download URL
    FILENAME="${BINARY_NAME}-${OS}-${ARCH}"
    if [ "$OS" = "windows" ]; then
        FILENAME="${FILENAME}.exe"
    fi

    DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${FILENAME}"

    info "Downloading ${FILENAME}..."

    # Create temp directory
    TMP_DIR=$(mktemp -d)
    trap "rm -rf $TMP_DIR" EXIT

    # Download
    if ! curl -fsSL "$DOWNLOAD_URL" -o "$TMP_DIR/$BINARY_NAME"; then
        error "Download failed from $DOWNLOAD_URL"
    fi

    # Verify integrity against the release's checksums.txt
    CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"
    if ! curl -fsSL "$CHECKSUMS_URL" -o "$TMP_DIR/checksums.txt"; then
        error "Failed to download checksums.txt from $CHECKSUMS_URL"
    fi
    verify_checksum "$TMP_DIR/$BINARY_NAME" "$FILENAME" "$TMP_DIR/checksums.txt"
    info "Checksum verified"

    # Make executable
    chmod +x "$TMP_DIR/$BINARY_NAME"

    TARGET="${INSTALL_DIR}/${BINARY_NAME}"
    if [ -w "$INSTALL_DIR" ]; then
        mv "$TMP_DIR/$BINARY_NAME" "$TARGET"
    else
        # Replace with mv (never copy over the old file in place: that breaks
        # a running "bbkt mcp" and macOS code signatures).
        info "Need sudo to install to ${INSTALL_DIR} (or rerun with --user, or set INSTALL_DIR)"
        sudo mv "$TMP_DIR/$BINARY_NAME" "$TARGET"
    fi
    success "Installed to ${TARGET}"

    # Verify installation
    if has_command "$BINARY_NAME"; then
        INSTALLED_PATH="$TARGET"
        ON_PATH=$(command -v "$BINARY_NAME")
        success "Installation complete!"
        echo ""
        printf '  Binary:  %b%s%b\n' "$GREEN" "$INSTALLED_PATH" "$NC"
        printf '  Version: %b%s%b\n' "$GREEN" "$VERSION" "$NC"
        if [ "$ON_PATH" != "$TARGET" ]; then
            warn "'${BINARY_NAME}' on your PATH is ${ON_PATH}, not ${TARGET}"
        fi
        echo ""
        echo "  Next steps:"
        echo "  1. Authenticate:  ${BINARY_NAME} auth           (Atlassian API token, default)"
        echo "                or  ${BINARY_NAME} auth --oauth   (OAuth 2.0 browser flow)"
        echo "  2. Verify:        ${BINARY_NAME} status"
        echo "  3. Configure your MCP client (Claude Desktop, Cursor, etc.)"
        echo "     to launch: ${INSTALLED_PATH} mcp"
        echo ""
        echo "  Docs: https://github.com/${REPO}"
        echo ""
    else
        warn "Installed but ${BINARY_NAME} not in PATH"
        echo "  Add ${INSTALL_DIR} to your PATH, or run directly:"
        echo "  ${INSTALL_DIR}/${BINARY_NAME}"
    fi
}

# Allow sourcing the functions (for tests) without running the installer
if [ "${BBKT_INSTALL_SOURCE_ONLY:-}" = "1" ]; then
    return 0 2>/dev/null || exit 0
fi

# Run with version flag
if [ "$1" = "--version" ] || [ "$1" = "-v" ]; then
    get_latest_version
    exit 0
fi

# Run with help flag
if [ "$1" = "--help" ] || [ "$1" = "-h" ]; then
    echo "bbkt installer"
    echo ""
    echo "Usage:"
    echo "  curl -sSL https://raw.githubusercontent.com/${REPO}/main/install.sh | bash"
    echo "  curl -sSL ... | bash -s -- --user   # Install to ~/.local/bin (no sudo)"
    echo ""
    echo "Options:"
    echo "  --user           Install to ~/.local/bin (no sudo required)"
    echo "  --version, -v    Print latest version"
    echo "  --help, -h       Show this help"
    echo ""
    echo "Environment variables:"
    echo "  INSTALL_DIR      Installation directory (default: /usr/local/bin)"
    echo ""
    exit 0
fi

main
