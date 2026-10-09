#!/usr/bin/env bash
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
CONTENT="$ROOT/lazycat/content"
OUT_BIN="$HERE/nexterm-server"

LZC_ARCH="${LZC_ARCH:-amd64}"
case "$LZC_ARCH" in
  amd64) ELF_ARCH="x86-64" ;;
  arm64) ELF_ARCH="aarch64" ;;
  *)
    echo "[lzc-build] 错误：LZC_ARCH 只能是 amd64 或 arm64，收到：$LZC_ARCH" >&2
    exit 1
    ;;
esac

echo "[lzc-build] 仓库根: $ROOT"
echo "[lzc-build] 目标架构: ${LZC_ARCH}（linux/${LZC_ARCH}，Go 交叉编译）"

# 版本解析：NEXTERM_RELEASE_VERSION 优先，其次当前提交恰好落在 tag 上；解析到就同时
# 注入 package.yml 与 build.mjs（二进制烙同一版本），解析不到保持 0.0.0 占位。
LZC_VERSION="${NEXTERM_RELEASE_VERSION:-}"
if [ -z "$LZC_VERSION" ]; then
  LZC_VERSION="$(git -C "$ROOT" describe --exact-match --tags HEAD 2>/dev/null | sed 's/^v//' || true)"
fi
if [ -n "$LZC_VERSION" ]; then
  export NEXTERM_RELEASE_VERSION="$LZC_VERSION"
  sed -i "s/^version: .*/version: ${LZC_VERSION}/" "$ROOT/lazycat/package.yml"
  echo "[lzc-build] 包版本: ${LZC_VERSION}（tag/env 注入，package.yml 与二进制一致）"
else
  echo "[lzc-build] 包版本: 保持 0.0.0 占位（非 tag 构建）"
fi

BASE_REF="$(grep -E '^FROM ' "$HERE/Dockerfile" | tail -1 | awk '{print $NF}')"
echo "[lzc-build] 1/4 基线镜像: ${BASE_REF}"
case "$BASE_REF" in
  registry.lazycat.cloud/*) ;;
  *)
    echo "[lzc-build] 警告：FROM 不是懒猫官方源引用，上游匹配会失败，整个基线层将被全量内嵌" >&2
    echo "[lzc-build]    修复：先跑  lzc-cli appstore copy-image <基线> --arch ${LZC_ARCH}" >&2
    echo "[lzc-build]    再把 FROM 改成它打印出的 registry.lazycat.cloud/... 引用。" >&2
    echo "[lzc-build]    （不是致命错误：LPK 仍能构建安装，只是基线层全量内嵌会明显增大体积。）" >&2
    ;;
esac

echo "[lzc-build] 2/4 构建前端（node scripts/build.mjs frontend → dist/）"
cd "$ROOT"

if [ -n "${NEXTERM_FORCE_INSTALL:-}" ] || [ ! -d node_modules ]; then
  if [ -f pnpm-lock.yaml ]; then
    pnpm install --frozen-lockfile
  else
    pnpm install
  fi
else
  echo "[lzc-build]     node_modules 已存在，跳过 pnpm install（强制重装：NEXTERM_FORCE_INSTALL=1）"
fi

node scripts/build.mjs frontend

rm -rf "$CONTENT/web"
mkdir -p "$CONTENT"
cp -R "$ROOT/dist" "$CONTENT/web"
echo "[lzc-build]     前端就位: $CONTENT/web"

rm -rf "$CONTENT/lazycat-injects"
mkdir -p "$CONTENT/lazycat-injects"
cp "$HERE"/../injects/*.js "$CONTENT/lazycat-injects/"
echo "[lzc-build]     注入脚本就位: $CONTENT/lazycat-injects"

echo "[lzc-build] 3/4 构建服务端二进制（linux/${LZC_ARCH}，node scripts/build.mjs server，可能需要几分钟）"
node scripts/build.mjs server --release --os=linux --arch="$LZC_ARCH"

GO_BIN="$ROOT/target/go-build/nexterm-server-linux-${LZC_ARCH}"
install -m 0755 "$GO_BIN" "$OUT_BIN"
echo "[lzc-build]     二进制就位: $OUT_BIN"

echo "[lzc-build] 4/4 自检"
FILE_OUT="$(file -b "$OUT_BIN" || true)"
case "$FILE_OUT" in
  *ELF*"$ELF_ARCH"*) ;;
  *)
    echo "[lzc-build] 错误：产出不是 Linux/${ELF_ARCH} 的 ELF：$FILE_OUT" >&2
    echo "[lzc-build]   目标架构是 LZC_ARCH=${LZC_ARCH}。" >&2
    exit 1
    ;;
esac
case "$FILE_OUT" in
  *statically\ linked*) ;;
  *)
    echo "[lzc-build] 错误：产出不是静态链接：$FILE_OUT" >&2
    echo "[lzc-build]   服务端必须 CGO_ENABLED=0（共享交付入口已强制，这里不应发生）。" >&2
    exit 1
    ;;
esac
if command -v go >/dev/null 2>&1; then
  if ! go version -m "$OUT_BIN" | grep -q 'CGO_ENABLED=0'; then
    echo "[lzc-build] 错误：go version -m 里没有 CGO_ENABLED=0 标记" >&2
    exit 1
  fi
fi
if [ ! -f "$CONTENT/web/index.html" ]; then
  echo "[lzc-build] 错误：$CONTENT/web/index.html 不存在：前端没构建成功？" >&2
  exit 1
fi

echo "[lzc-build] 完成。接下来：lzc-cli project build"
