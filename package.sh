#!/usr/bin/env bash
set -euo pipefail

APP=monitor-imap-webhook
BIN_NAME=monitor
VERSION=${VERSION:-$(git describe --tags --always --dirty || echo 0.1.0)}
REVISION=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
ARCH=${ARCH:-amd64}
OS=${OS:-linux}
PREFIX=${PREFIX:-/usr/local}
ROOT_DIR=$(pwd)
OUT_DIR=$ROOT_DIR/dist

mkdir -p "$OUT_DIR"

cd "$PKG_DIR"

echo "==> Building Go binary ($OS/$ARCH)"
GOOS=$OS GOARCH=$ARCH CGO_ENABLED=0 go build -ldflags="-s -w -X main.buildVersion=$VERSION -X main.buildRevision=$REVISION -X main.buildTime=$BUILD_TIME" -o "$OUT_DIR/$BIN_NAME" ./cmd/monitor

if ! command -v fpm >/dev/null 2>&1; then
  echo "fpm 未安装，请先安装 (gem install --no-document fpm)" >&2
  exit 1
fi

PKG_DIR=$OUT_DIR/pkgroot
POSTINST=$OUT_DIR/postinstall.sh
PRERM=$OUT_DIR/prerm.sh
rm -rf "$PKG_DIR"
mkdir -p "$PKG_DIR$PREFIX/bin"
cp "$OUT_DIR/$BIN_NAME" "$PKG_DIR$PREFIX/bin/$BIN_NAME"
chmod 0755 "$PKG_DIR$PREFIX/bin/$BIN_NAME"

# 示例配置模板（不包含敏感信息）
mkdir -p "$PKG_DIR/etc/$APP"
cp config.example.yaml "$PKG_DIR/etc/$APP/config.example.yaml"
# systemd service
install -Dm0644 packaging/monitor-imap-webhook.service "$PKG_DIR/lib/systemd/system/monitor-imap-webhook.service"

DESCRIPTION="IMAP mailbox monitor that pushes parsed emails to a webhook (Go)"

cat > "$POSTINST" <<'EOF'
#!/bin/sh
set -e
if [ ! -f /etc/monitor-imap-webhook/config.yaml ]; then
  cp /etc/monitor-imap-webhook/config.example.yaml /etc/monitor-imap-webhook/config.yaml || true
fi
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
  systemctl enable monitor-imap-webhook.service || true
  echo "Edit /etc/monitor-imap-webhook/config.yaml then: systemctl start monitor-imap-webhook"
fi
EOF
chmod 0755 "$POSTINST"

cat > "$PRERM" <<'EOF'
#!/bin/sh
set -e
if command -v systemctl >/dev/null 2>&1; then
  systemctl stop monitor-imap-webhook.service 2>/dev/null || true
  systemctl disable monitor-imap-webhook.service 2>/dev/null || true
  systemctl daemon-reload 2>/dev/null || true
fi
EOF
chmod 0755 "$PRERM"

COMMON_FPM_ARGS=(
  -s dir -C "$PKG_DIR"
  --name "$APP"
  --version "$VERSION"
  --architecture "$ARCH"
  --description "$DESCRIPTION"
  --url "https://example.com/$APP"
  --license "Proprietary"
  --maintainer "Your Name <you@example.com>"
  --config-files /etc/$APP/config.example.yaml
  --after-install "$POSTINST"
  --before-remove "$PRERM"
)

mkdir -p "$OUT_DIR"

echo "==> Debug: listing package root" >&2
find "$PKG_DIR" -maxdepth 4 -type f -print >&2 || true

# deb
fpm "${COMMON_FPM_ARGS[@]}" -t deb -p "$OUT_DIR/$APP"_VERSION_ARCH.deb .
# rpm
fpm "${COMMON_FPM_ARGS[@]}" -t rpm -p "$OUT_DIR/$APP"-VERSION.ARCH.rpm .

cd "$OUT_DIR"
for f in $APP*_VERSION_*; do
  nv=${f/_VERSION_/$VERSION}
  mv "$f" "$nv"; echo "Created $nv"; done || true
for f in $APP*-VERSION.*; do
  nv=${f/-VERSION./-$VERSION.}
  mv "$f" "$nv"; echo "Created $nv"; done || true
cd "$ROOT_DIR"

echo "==> Packages located in $OUT_DIR"