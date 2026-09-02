#!/bin/sh
# Install brain-axi.
#
# Piped from the web, this downloads the newest published release binary for
# this platform and verifies it against the release's checksum manifest:
#
#   curl -fsSL https://raw.githubusercontent.com/Thanhbinh1905/secondbrain/main/install.sh | sh
#
# Run from inside a checkout, it builds that checkout instead, and records it so
# `brain-axi update` knows what to fast-forward later:
#
#   ./install.sh
#
# Either mode can be demanded explicitly, so nothing depends on detection:
#
#   ./install.sh --checkout   build from the checkout this script lives in
#   ./install.sh --release    download the newest published binary
#
# Environment:
#   BRAIN_AXI_INSTALL_MODE  checkout | release          (default: see above)
#   BRAIN_AXI_INSTALL_DIR   where the binary goes       (default ~/.brain-axi/bin)
#   BRAIN_AXI_LINK_DIR      where the PATH link goes    (default the first of
#                           ~/.local/bin or /usr/local/bin)
set -eu

REPO="github.com/Thanhbinh1905/secondbrain"
RELEASE_BASE="https://$REPO/releases/latest/download"
CHECKSUMS="checksums.txt"

INSTALL_DIR="${BRAIN_AXI_INSTALL_DIR:-$HOME/.brain-axi/bin}"
LINK_DIR="${BRAIN_AXI_LINK_DIR:-}"
MODE="${BRAIN_AXI_INSTALL_MODE:-}"

for arg in "$@"; do
  case "$arg" in
    --checkout) MODE=checkout ;;
    --release) MODE=release ;;
    -h|--help)
      cat <<'USAGE'
usage: install.sh [--checkout | --release]

  --checkout  build from the checkout this script lives in
  --release   download and verify the newest published release binary

With neither, a script run from inside a checkout builds it and a script piped
from the web downloads a release.

  BRAIN_AXI_INSTALL_MODE  checkout | release
  BRAIN_AXI_INSTALL_DIR   where the binary goes     (default ~/.brain-axi/bin)
  BRAIN_AXI_LINK_DIR      where the PATH link goes  (default the first of
                          ~/.local/bin or /usr/local/bin)
USAGE
      exit 0
      ;;
    *)
      echo "install.sh: unknown argument '$arg'; expected --checkout or --release" >&2
      exit 1
      ;;
  esac
done

# SOURCE_DIR is set only when this script is running from a file inside a clone
# of this repository. Piped through sh there is no script file at all, which is
# exactly the case that must not try to build.
SOURCE_DIR=""
if [ -f "$0" ]; then
  candidate="$(cd "$(dirname "$0")" && pwd -P)"
  if [ -f "$candidate/go.mod" ] && grep -q "^module $REPO\$" "$candidate/go.mod" 2>/dev/null; then
    SOURCE_DIR="$candidate"
  fi
fi

if [ -z "$MODE" ]; then
  if [ -n "$SOURCE_DIR" ]; then MODE=checkout; else MODE=release; fi
fi

case "$MODE" in
  checkout|release) ;;
  *)
    echo "install.sh: BRAIN_AXI_INSTALL_MODE is '$MODE'; expected checkout or release" >&2
    exit 1
    ;;
esac

if [ "$MODE" = checkout ] && [ -z "$SOURCE_DIR" ]; then
  echo "install.sh: --checkout needs this script to be run from inside a secondbrain checkout." >&2
  echo "Clone $REPO and run ./install.sh from it, or drop --checkout to download a release." >&2
  exit 1
fi

if [ -z "$LINK_DIR" ]; then
  case ":$PATH:" in
    *":$HOME/.local/bin:"*) LINK_DIR="$HOME/.local/bin" ;;
    *) LINK_DIR="/usr/local/bin" ;;
  esac
fi

BIN_PATH="$INSTALL_DIR/brain-axi"
LINK_PATH="$LINK_DIR/brain-axi"

mkdir -p "$INSTALL_DIR"
if ! TMPDIR_INSTALL="$(mktemp -d "$INSTALL_DIR/.brain-axi-install.XXXXXX")"; then
  echo "Could not create a staging directory in $INSTALL_DIR." >&2
  exit 1
fi
STAGED="$TMPDIR_INSTALL/brain-axi"
METHOD_PATH="$INSTALL_DIR/.brain-axi-install"
METHOD_PUBLISHED=0
BINARY_PUBLISHED=0

remove_method_record() {
  if ! rm -f "$METHOD_PATH"; then
    echo "Could not remove $METHOD_PATH after installation failed; it may name the wrong install method." >&2
    return 1
  fi
  echo "Removed $METHOD_PATH after installation failed; the install method is unrecorded. Re-run the installer." >&2
}

metadata_failure() {
  echo "$1" >&2
  exit 1
}

cleanup() {
  status=$?
  if ! rm -rf "$TMPDIR_INSTALL"; then
    echo "Could not remove the staging directory $TMPDIR_INSTALL." >&2
    status=1
  fi
  if [ "$METHOD_PUBLISHED" -eq 1 ] && [ "$BINARY_PUBLISHED" -eq 0 ]; then
    if ! remove_method_record; then
      status=1
    fi
  fi
  trap - EXIT
  exit "$status"
}

trap cleanup EXIT
trap 'exit 1' HUP INT TERM

# --- download helpers -------------------------------------------------------

# fetch URL FILE, through the download tool picked below. Both are told to fail
# on an HTTP error rather than writing the error page to disk, and both follow
# the redirect that releases/latest is.
fetch() {
  case "$DOWNLOAD" in
    curl) curl --fail --silent --show-error --location --output "$2" "$1" ;;
    wget) wget --quiet --output-document "$2" "$1" ;;
  esac
}

# sha256 FILE, through the checksum tool picked below: GNU coreutils has
# sha256sum and macOS has shasum.
sha256() {
  case "$SHA" in
    sha256sum) sha256sum "$1" | cut -d' ' -f1 ;;
    shasum) shasum -a 256 "$1" | cut -d' ' -f1 ;;
  esac
}

# --- install ----------------------------------------------------------------

if [ "$MODE" = checkout ]; then
  if ! command -v go >/dev/null 2>&1; then
    echo "Go is required to build brain-axi from a checkout and is not on PATH." >&2
    echo "Install Go 1.26 or newer, or run ./install.sh --release to download a binary." >&2
    exit 1
  fi

  VERSION="$(git -C "$SOURCE_DIR" rev-parse --short=12 HEAD 2>/dev/null || echo dev)"
  if ! git -C "$SOURCE_DIR" diff --quiet 2>/dev/null; then
    VERSION="$VERSION-dirty"
  fi

  echo "Building brain-axi $VERSION from $SOURCE_DIR..."
  ( cd "$SOURCE_DIR" && go build -ldflags "-X main.version=$VERSION" -o "$STAGED" ./cmd/brain-axi )
else
  # Both tools are resolved before anything is downloaded, because a host that
  # cannot verify a download must refuse before it has one to install rather
  # than after. An unverified binary is never installed.
  if command -v curl >/dev/null 2>&1; then
    DOWNLOAD=curl
  elif command -v wget >/dev/null 2>&1; then
    DOWNLOAD=wget
  else
    echo "Neither curl nor wget is on PATH, so nothing can be downloaded." >&2
    echo "Install one of them, or clone $REPO and run ./install.sh --checkout." >&2
    exit 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    SHA=sha256sum
  elif command -v shasum >/dev/null 2>&1; then
    SHA=shasum
  else
    echo "Neither sha256sum nor shasum is on PATH, so a download cannot be verified." >&2
    echo "brain-axi refuses to install a binary it has not checked; clone $REPO and run ./install.sh --checkout instead." >&2
    exit 1
  fi

  # The asset name is what `uname -s` and `uname -m` print on this machine.
  # Release assets are named that way on purpose, so this script needs no
  # platform table of its own to keep in sync with the release workflow.
  ASSET="brain-axi_$(uname -s)_$(uname -m)"

  echo "Downloading the newest brain-axi release..."
  if ! fetch "$RELEASE_BASE/$CHECKSUMS" "$TMPDIR_INSTALL/$CHECKSUMS"; then
    echo "Could not download $RELEASE_BASE/$CHECKSUMS." >&2
    echo "If $REPO has no published release yet, there is nothing to download; clone it and run ./install.sh --checkout." >&2
    exit 1
  fi

  # The manifest is the release's own list of what it published, so a platform
  # this release does not cover is named from the release rather than guessed.
  if ! WANT="$(grep -E "[ *]${ASSET}\$" "$TMPDIR_INSTALL/$CHECKSUMS" | cut -d' ' -f1)" || [ -z "$WANT" ]; then
    echo "No brain-axi binary is published for $(uname -s)/$(uname -m) (looked for $ASSET)." >&2
    echo "The newest release publishes:" >&2
    sed 's/^[0-9a-f]*[ *]*/  /' "$TMPDIR_INSTALL/$CHECKSUMS" >&2
    exit 1
  fi

  if ! fetch "$RELEASE_BASE/$ASSET" "$STAGED"; then
    echo "Could not download $RELEASE_BASE/$ASSET." >&2
    exit 1
  fi

  GOT="$(sha256 "$STAGED")"
  if [ "$GOT" != "$WANT" ]; then
    echo "$ASSET hashes to $GOT but the published $CHECKSUMS says $WANT." >&2
    echo "Nothing was installed. Do not run this download; report it instead." >&2
    exit 1
  fi
  echo "Verified $ASSET against the published $CHECKSUMS."
fi

chmod 755 "$STAGED"

# Refuse to install a binary that does not run, or that is not this one.
if ! REPORTED="$("$STAGED" --version)"; then
  echo "The binary does not run, so nothing was installed." >&2
  exit 1
fi
case "$REPORTED" in
  "brain-axi "*) ;;
  *)
    echo "The binary reported '$REPORTED' instead of a brain-axi version, so nothing was installed." >&2
    exit 1
    ;;
esac
VERSION="${REPORTED#brain-axi }"

# Record how this installed, so `brain-axi update` never has to guess: guessing
# means either replacing a binary the user did not install that way, or telling
# them to run a command that cannot work.
METHOD_STAGED="$TMPDIR_INSTALL/.brain-axi-install"
if ! printf '%s\n' "$MODE" > "$METHOD_STAGED"; then
  metadata_failure "Could not write the install method record in $INSTALL_DIR; nothing was installed."
fi
if [ "$MODE" = checkout ]; then
  # Record the checkout so `brain-axi update` knows what to fast-forward.
  SOURCE_STAGED="$TMPDIR_INSTALL/.brain-axi-source"
  if ! printf '%s\n' "$SOURCE_DIR" > "$SOURCE_STAGED"; then
    metadata_failure "Could not write the source checkout record in $INSTALL_DIR; nothing was installed."
  fi
  if ! mv "$METHOD_STAGED" "$METHOD_PATH"; then
    metadata_failure "Could not publish the install method record in $INSTALL_DIR; nothing was installed."
  fi
  METHOD_PUBLISHED=1
  if ! mv "$SOURCE_STAGED" "$INSTALL_DIR/.brain-axi-source"; then
    metadata_failure "Could not publish the source checkout record in $INSTALL_DIR; nothing was installed."
  fi
else
  if ! mv "$METHOD_STAGED" "$METHOD_PATH"; then
    metadata_failure "Could not publish the install method record in $INSTALL_DIR; nothing was installed."
  fi
  METHOD_PUBLISHED=1
  # A previous checkout install may have left one behind, and it names a
  # checkout this binary did not come from.
  if ! rm -f "$INSTALL_DIR/.brain-axi-source"; then
    metadata_failure "Could not clear the stale source checkout record in $INSTALL_DIR; nothing was installed."
  fi
fi
if ! mv "$STAGED" "$BIN_PATH"; then
  echo "Could not replace $BIN_PATH." >&2
  exit 1
fi
BINARY_PUBLISHED=1

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
if [ "$MODE" = checkout ]; then
  echo "Source checkout: ${SOURCE_DIR}"
  echo "Upgrade with: brain-axi update  (fast-forwards that checkout and rebuilds)"
else
  echo "Upgrade with: brain-axi update  (downloads and verifies the newest release)"
fi

case ":$PATH:" in
  *":$LINK_DIR:"*) ;;
  *) echo "Add ${LINK_DIR} to your PATH and restart your terminal." ;;
esac

echo
echo "Next:"
echo "  brain-axi init                      create the vault"
echo "  brain-axi setup skill --claude       teach your agent about it"
echo "  brain-axi doctor                     check everything"
