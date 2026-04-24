#!/usr/bin/env bash
# install-cli: download and install the Mortise CLI binary
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/mortise-org/mortise/main/scripts/install-cli.sh | bash
#
# Detects OS and architecture, downloads the matching binary from the latest
# GitHub release, and installs it to /usr/local/bin (or ~/.local/bin).
set -euo pipefail

REPO="mortise-org/mortise"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
info()  { printf '\033[1;34m[mortise]\033[0m %s\n' "$*"; }
error() { printf '\033[1;31m[mortise]\033[0m %s\n' "$*" >&2; exit 1; }

command_exists() { command -v "$1" >/dev/null 2>&1; }

# ---------------------------------------------------------------------------
# Detect OS + architecture
# ---------------------------------------------------------------------------
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$OS" in
    linux)  ;;
    darwin) ;;
    *)      error "Unsupported OS: $OS" ;;
esac

case "$ARCH" in
    x86_64)       ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *)            error "Unsupported architecture: $ARCH" ;;
esac

info "Detected platform: ${OS}/${ARCH}"

# ---------------------------------------------------------------------------
# Resolve latest version
# ---------------------------------------------------------------------------
VERSION="${MORTISE_CLI_VERSION:-}"
if [ -z "$VERSION" ]; then
    info "Fetching latest release..."
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' | cut -d'"' -f4) \
        || error "Failed to fetch latest release. Set MORTISE_CLI_VERSION to install a specific version."
fi

[ -n "$VERSION" ] || error "Could not determine version"
info "Installing mortise ${VERSION}..."

# ---------------------------------------------------------------------------
# Download binary
# ---------------------------------------------------------------------------
BINARY_NAME="mortise-${OS}-${ARCH}"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${BINARY_NAME}"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

curl -fsSL -o "${TMPDIR}/mortise" "$DOWNLOAD_URL" \
    || error "Failed to download ${DOWNLOAD_URL}"
chmod +x "${TMPDIR}/mortise"

# ---------------------------------------------------------------------------
# Install binary
# ---------------------------------------------------------------------------
INSTALL_DIR="/usr/local/bin"
if [ ! -w "$INSTALL_DIR" ] 2>/dev/null; then
    INSTALL_DIR="${HOME}/.local/bin"
    mkdir -p "$INSTALL_DIR"
fi

mv "${TMPDIR}/mortise" "${INSTALL_DIR}/mortise"

info "Installed mortise to ${INSTALL_DIR}/mortise"

# Check if install dir is on PATH
case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) ;;
    *) printf '\033[1;33m[mortise]\033[0m %s\n' \
        "Add ${INSTALL_DIR} to your PATH: export PATH=\"${INSTALL_DIR}:\$PATH\"" ;;
esac

info "Run 'mortise --help' to get started."
