#!/bin/sh
# Install brain-axi from this checkout.
#
# The binary is built here rather than downloaded: there are no published
# release artefacts, and building from the checkout is also what records where
# that checkout lives, which is what `brain-axi update` fast-forwards later.
#
#   ./install.sh
#
# Environment:
#   BRAIN_AXI_INSTALL_DIR   where the binary goes      (default ~/.brain-axi/bin)
#   BRAIN_AXI_LINK_DIR      where the PATH link goes   (default the first of
#                           ~/.local/bin or /usr/local/bin)
set -e

SOURCE_DIR="$(cd "$(dirname "$0")" && pwd -P)"
INSTALL_DIR="${BRAIN_AXI_INSTALL_DIR:-$HOME/.brain-axi/bin}"
LINK_DIR="${BRAIN_AXI_LINK_DIR:-}"

if [ -z "$LINK_DIR" ]; then
  case ":$PATH:" in
    *":$HOME/.local/bin:"*) LINK_DIR="$HOME/.local/bin" ;;
    *) LINK_DIR="/usr/local/bin" ;;
  esac
fi

BIN_PATH="$INSTALL_DIR/brain-axi"
LINK_PATH="$LINK_DIR/brain-axi"

if [ ! -f "$SOURCE_DIR/go.mod" ]; then
  echo "install.sh must be run from inside the secondbrain checkout" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "Go is required to build brain-axi and is not on PATH." >&2
  echo "Install Go 1.26 or newer, then run this script again." >&2
  exit 1
fi

VERSION="$(git -C "$SOURCE_DIR" rev-parse --short=12 HEAD 2>/dev/null || echo dev)"
if ! git -C "$SOURCE_DIR" diff --quiet 2>/dev/null; then
  VERSION="$VERSION-dirty"
fi

mkdir -p "$INSTALL_DIR"

TMPDIR_BUILD="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_BUILD"' EXIT

echo "Building brain-axi $VERSION from $SOURCE_DIR..."
( cd "$SOURCE_DIR" && go build -ldflags "-X main.version=$VERSION" -o "$TMPDIR_BUILD/brain-axi" ./cmd/brain-axi )

# Refuse to install a binary that does not run.
if ! "$TMPDIR_BUILD/brain-axi" --version >/dev/null; then
  echo "The binary that was just built does not run; nothing was installed." >&2
  exit 1
fi

mv "$TMPDIR_BUILD/brain-axi" "$BIN_PATH"
chmod 755 "$BIN_PATH"

# Record the checkout so `brain-axi update` knows what to fast-forward.
printf '%s\n' "$SOURCE_DIR" > "$INSTALL_DIR/.brain-axi-source"

resolve_path() {
  (cd "$1" 2>/dev/null && pwd -P)
}

REAL_INSTALL_DIR="$(resolve_path "$INSTALL_DIR")"
REAL_LINK_DIR="$(resolve_path "$LINK_DIR" 2>/dev/null || echo "")"

if [ -n "$REAL_INSTALL_DIR" ] && [ "$REAL_INSTALL_DIR" = "$REAL_LINK_DIR" ]; then
  echo "Install dir and link dir resolve to the same path; skipping symlink."
else
  if [ -w "$LINK_DIR" ] || (mkdir -p "$LINK_DIR" 2>/dev/null && [ -w "$LINK_DIR" ]); then
    rm -f "$LINK_PATH"
    ln -s "$BIN_PATH" "$LINK_PATH"
  else
    echo "Linking ${LINK_PATH} to ${BIN_PATH} (requires sudo)..."
    sudo mkdir -p "$LINK_DIR"
    sudo rm -f "$LINK_PATH"
    sudo ln -s "$BIN_PATH" "$LINK_PATH"
  fi
fi

echo "brain-axi $VERSION installed to ${BIN_PATH}"
echo "Command path: ${LINK_PATH} -> ${BIN_PATH}"
echo "Source checkout: ${SOURCE_DIR}"

case ":$PATH:" in
  *":$LINK_DIR:"*) ;;
  *) echo "Add ${LINK_DIR} to your PATH and restart your terminal." ;;
esac

echo
echo "Next:"
echo "  brain-axi init                      create the vault"
echo "  brain-axi setup skill --claude       teach your agent about it"
echo "  brain-axi doctor                     check everything"
