#!/bin/sh
# NexTerm device agent one-line installer (Linux / macOS).
# Usage: curl -fsSL https://<server>/install-device.sh | sh -s -- --server <server URL> --code <enrollment code> --version <version> [--insecure] [--data-dir <dir>]
# Supports Linux/macOS x86_64/aarch64 (arm64) only. The binary is installed only after verification against the same release's SHA256SUMS, then the device is enrolled and a per-user background service is installed (Linux=systemd --user, macOS=launchd); no root required.
set -eu

RELEASE_BASE="https://github.com/Hello-CTF/NexTerm/releases/download"

die() { printf 'install-device: %s\n' "$*" >&2; exit 1; }

usage() {
  printf '%s\n' \
    '用法: install-device.sh --server <接入地址> --code <接入码> --version <版本> [--insecure] [--data-dir <目录>]' \
    '' \
    '从 NexTerm GitHub release 下载同版本 nexterm-agent 资产 (Linux/macOS), 用同 release 的 SHA256SUMS 校验后安装到 ~/.local/bin, 再 enroll 设备并安装 per-user 后台服务 (Linux 为 systemd --user, macOS 为 launchd)。' \
    '' \
    '  --server    NexTerm 接入地址 (必填), 如 https://nexterm.example.com' \
    '  --code      设备接入码 (必填), 设备管理页签发的一次性接入码' \
    '  --version   NexTerm 版本 (必填), 如 0.2.2-rc.7 (对应 GitHub release tag v0.2.2-rc.7)' \
    '  --insecure  接入地址使用自签名证书时跳过 TLS 校验' \
    '  --data-dir  数据目录 (默认 ${NEXTERM_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/NexTerm})'
}

usage_error() {
  printf 'install-device: %s\n' "$*" >&2
  printf '用法: install-device.sh --server <接入地址> --code <接入码> --version <版本> [--insecure] [--data-dir <目录>]\n' >&2
  exit 2
}

SERVER="" CODE="" VERSION="" DATA_DIR_ARG="" INSECURE_ARG=""
while [ $# -gt 0 ]; do
  case "$1" in
    --server) [ $# -ge 2 ] || usage_error "--server 需要一个值"; SERVER="$2"; shift 2 ;;
    --server=*) SERVER="${1#--server=}"; shift ;;
    --code) [ $# -ge 2 ] || usage_error "--code 需要一个值"; CODE="$2"; shift 2 ;;
    --code=*) CODE="${1#--code=}"; shift ;;
    --version) [ $# -ge 2 ] || usage_error "--version 需要一个值"; VERSION="$2"; shift 2 ;;
    --version=*) VERSION="${1#--version=}"; shift ;;
    --data-dir) [ $# -ge 2 ] || usage_error "--data-dir 需要一个值"; DATA_DIR_ARG="$2"; shift 2 ;;
    --data-dir=*) DATA_DIR_ARG="${1#--data-dir=}"; shift ;;
    --insecure) INSECURE_ARG="--insecure"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage_error "未知参数 $1" ;;
  esac
done

[ -n "$SERVER" ] || usage_error "缺少 --server (NexTerm 接入地址)"
[ -n "$CODE" ] || usage_error "缺少 --code (设备接入码)"
[ -n "$VERSION" ] || usage_error "缺少 --version (NexTerm 版本)"
case "$VERSION" in
  *[!0-9A-Za-z.-]*) usage_error "非法 --version: $VERSION" ;;
esac

[ "$(id -u)" -ne 0 ] || die "拒绝以 root 运行: agent 是 per-user 服务 (systemd --user / launchd), 请用普通用户执行"

OS_NAME="$(uname -s)"
case "$OS_NAME" in
  Linux) OS="linux" ;;
  Darwin) OS="darwin" ;;
  *) die "仅支持 Linux/macOS, 当前系统: $OS_NAME" ;;
esac
MACHINE="$(uname -m)"
case "$MACHINE" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) die "不支持的 CPU 架构: $MACHINE (仅支持 x86_64/aarch64|arm64)" ;;
esac

[ -n "${HOME:-}" ] || die "HOME 未设置, 无法确定 per-user 安装目录"
DATA_DIR="${DATA_DIR_ARG:-${NEXTERM_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/NexTerm}}"
BIN_DIR="$HOME/.local/bin"
BINARY="$BIN_DIR/nexterm-agent"

if command -v sha256sum >/dev/null 2>&1; then
  sha256_of() { sha256sum "$1"; }
elif command -v shasum >/dev/null 2>&1; then
  sha256_of() { shasum -a 256 "$1"; }
else
  die "需要 sha256sum 或 shasum 校验下载"
fi
command -v tar >/dev/null 2>&1 || die "需要 tar 解压发布包"
if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
  die "需要 curl 或 wget 下载发布资产"
fi

ASSET="NexTerm-agent_${VERSION}_${OS}_${ARCH}.tar.gz"
ASSET_URL="$RELEASE_BASE/v$VERSION/$ASSET"
SUMS_URL="$RELEASE_BASE/v$VERSION/SHA256SUMS"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$2" "$1" || die "下载失败: $1"
  else
    wget -q -O "$2" "$1" || die "下载失败: $1"
  fi
}

printf '==> 下载 %s\n' "$ASSET_URL"
fetch "$ASSET_URL" "$WORK/$ASSET"
printf '==> 下载 %s\n' "$SUMS_URL"
fetch "$SUMS_URL" "$WORK/SHA256SUMS"

EXPECTED_SUM="$(awk -v name="$ASSET" '$2 == name { print $1; exit }' "$WORK/SHA256SUMS")"
[ -n "$EXPECTED_SUM" ] || die "SHA256SUMS 中没有 $ASSET 的条目, 拒绝安装"
case "$EXPECTED_SUM" in
  *[!0-9a-f]*) die "SHA256SUMS 中 $ASSET 的校验和不是合法 sha256" ;;
esac
[ "${#EXPECTED_SUM}" -eq 64 ] || die "SHA256SUMS 中 $ASSET 的校验和不是合法 sha256"
ACTUAL_SUM="$(sha256_of "$WORK/$ASSET")"
ACTUAL_SUM="${ACTUAL_SUM%% *}"
[ "$ACTUAL_SUM" = "$EXPECTED_SUM" ] || die "SHA256 校验失败 ($ASSET): 期望 $EXPECTED_SUM, 实际 $ACTUAL_SUM, 拒绝安装"

printf '==> 校验通过, 解压并安装到 %s\n' "$BINARY"
tar -xzf "$WORK/$ASSET" -C "$WORK" || die "解压失败: $ASSET"
[ -f "$WORK/nexterm-agent" ] || die "发布包 $ASSET 根目录缺少 nexterm-agent"
mkdir -p "$BIN_DIR"
install -m 0755 "$WORK/nexterm-agent" "$BINARY"

printf '==> 注册设备 enroll (接入码不在日志回显)\n'
printf '    %s enroll --server %s --code ******** --data-dir %s%s\n' "$BINARY" "$SERVER" "$DATA_DIR" "${INSECURE_ARG:+ --insecure}"
"$BINARY" enroll --server "$SERVER" --code "$CODE" $INSECURE_ARG --data-dir "$DATA_DIR"

if [ "$OS" = "darwin" ]; then
  printf '==> 安装用户级服务 (launchd)\n'
else
  printf '==> 安装用户级服务 (systemd --user)\n'
fi
"$BINARY" install --data-dir "$DATA_DIR"

printf '==> 完成。数据目录: %s\n' "$DATA_DIR"
printf '    查看状态: %s status --data-dir "%s"\n' "$BINARY" "$DATA_DIR"
