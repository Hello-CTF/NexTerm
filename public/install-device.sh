#!/bin/sh
# NexTerm 设备 agent 一键接入 (FLEET157)。
# 用法: curl -fsSL https://<server>/install-device.sh | sh -s -- --server <接入地址> --code <接入码> --version <版本> [--insecure] [--data-dir <目录>]
# 仅支持 Linux x86_64/aarch64。二进制与同 release 的 SHA256SUMS 校验通过后才安装并 enroll, 最后装 systemd --user 服务, 全程不需要 root。
set -eu

RELEASE_BASE="https://github.com/ProbiusOfficial/NexTerm/releases/download"

die() { printf 'install-device: %s\n' "$*" >&2; exit 1; }

usage() {
  printf '%s\n' \
    '用法: install-device.sh --server <接入地址> --code <接入码> --version <版本> [--insecure] [--data-dir <目录>]' \
    '' \
    '从 NexTerm GitHub release 下载同版本 nexterm-server Linux 资产, 用同 release 的 SHA256SUMS 校验后安装到 ~/.local/bin, 再 enroll 设备并安装 systemd --user 服务。' \
    '' \
    '  --server    NexTerm 接入地址 (必填), 如 https://nexterm.example.com' \
    '  --code      设备接入码 (必填), 设备管理页签发的一次性接入码' \
    '  --version   NexTerm 版本 (必填), 如 0.2.2' \
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

[ "$(id -u)" -ne 0 ] || die "拒绝以 root 运行: agent 是 per-user 服务 (systemd --user), 请用普通用户执行"

[ "$(uname -s)" = "Linux" ] || die "仅支持 Linux, 当前系统: $(uname -s)"
MACHINE="$(uname -m)"
case "$MACHINE" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) die "不支持的 CPU 架构: $MACHINE (仅支持 x86_64/aarch64)" ;;
esac

[ -n "${HOME:-}" ] || die "HOME 未设置, 无法确定 per-user 安装目录"
DATA_DIR="${DATA_DIR_ARG:-${NEXTERM_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/NexTerm}}"
BIN_DIR="$HOME/.local/bin"
BINARY="$BIN_DIR/nexterm-server"

command -v sha256sum >/dev/null 2>&1 || die "需要 sha256sum 校验下载"
command -v tar >/dev/null 2>&1 || die "需要 tar 解压发布包"
if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
  die "需要 curl 或 wget 下载发布资产"
fi

ASSET="NexTerm-server_${VERSION}_linux_${ARCH}.tar.gz"
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
ACTUAL_SUM="$(sha256sum "$WORK/$ASSET")"
ACTUAL_SUM="${ACTUAL_SUM%% *}"
[ "$ACTUAL_SUM" = "$EXPECTED_SUM" ] || die "SHA256 校验失败 ($ASSET): 期望 $EXPECTED_SUM, 实际 $ACTUAL_SUM, 拒绝安装"

printf '==> 校验通过, 解压并安装到 %s\n' "$BINARY"
tar -xzf "$WORK/$ASSET" -C "$WORK" || die "解压失败: $ASSET"
mkdir -p "$BIN_DIR"
install -m 0755 "$WORK/NexTerm-server_${VERSION}_linux_${ARCH}/nexterm-server" "$BINARY"

INSTALLED_VERSION="$("$BINARY" --version)" || die "无法执行 $BINARY --version"
[ "$INSTALLED_VERSION" = "$VERSION" ] || die "安装的二进制版本 ($INSTALLED_VERSION) 与请求版本 ($VERSION) 不一致"

printf '==> 注册设备 enroll (接入码不在日志回显)\n'
printf '    %s agent enroll --server %s --code ******** --data-dir %s%s\n' "$BINARY" "$SERVER" "$DATA_DIR" "${INSECURE_ARG:+ --insecure}"
"$BINARY" agent enroll --server "$SERVER" --code "$CODE" $INSECURE_ARG --data-dir "$DATA_DIR"

printf '==> 安装用户级服务 (systemd --user)\n'
"$BINARY" agent install --data-dir "$DATA_DIR"

printf '==> 完成。数据目录: %s\n' "$DATA_DIR"
printf '    查看状态: %s agent status --data-dir "%s"\n' "$BINARY" "$DATA_DIR"
