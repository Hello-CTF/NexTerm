#!/usr/bin/env bash
#
# 容器内执行的构建入口（由 build-server.sh 挂进来）。
#
# 为什么单独一个文件而不是 `docker run ... sh -c '一大段'`：
# 那段逻辑里有引号、变量展开和中文提示，塞进 `-c` 里既难读也容易被外层 shell 先解释掉。
# 放进文件后，宿主侧只负责把参数用 `-e` 传进来。
#
# 输入（都由宿主侧以环境变量给出）：
#   NEXTERM_BUILD_TARGET   目标 triple，如 x86_64-unknown-linux-gnu
#   NEXTERM_CROSS_PREFIX   交叉工具链的**程序前缀**，如 x86_64-linux-gnu（同架构时用不到）
#   NEXTERM_GCC_CROSS_PKG  apt 的**包名**，如 gcc-x86-64-linux-gnu ← 注意是连字符，不是下划线
#   NEXTERM_LIBC_CROSS_PKG libc 交叉开发包名，如 libc6-dev-amd64-cross
#   CC_/AR_/CARGO_TARGET_*_LINKER  由宿主侧按需设置
#
# 包名与程序前缀的拼写**故意分开传**：Debian 里程序叫 `x86_64-linux-gnu-gcc`
# 而包叫 `gcc-x86-64-linux-gnu`，用同一个变量会得到
# `E: Unable to locate package gcc-x86_64-linux-gnu` —— 看着像源里没有，其实是拼错。

set -euo pipefail

TARGET="${NEXTERM_BUILD_TARGET:?必须由宿主侧提供 NEXTERM_BUILD_TARGET}"

# `uname -m` 给的是 aarch64，Debian 包名用的是 arm64 —— 归一化成 Debian 的叫法。
host_debian_arch() {
  case "$(uname -m)" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *) uname -m ;;
  esac
}

target_debian_arch() {
  case "$1" in
    x86_64-unknown-linux-gnu) echo amd64 ;;
    aarch64-unknown-linux-gnu) echo arm64 ;;
    *) echo "" ;;
  esac
}

WANT="$(target_debian_arch "$TARGET")"
if [ -z "$WANT" ]; then
  # 注意 `${TARGET}` 的花括号不能省：紧跟全角括号时 bash 会把多字节首字节吞进变量名。
  echo "[bin] ✗ 不认识的目标 triple：${TARGET}（只支持 x86_64 / aarch64 的 linux-gnu）" >&2
  exit 1
fi

HOST="$(host_debian_arch)"
echo "[bin] 容器架构 ${HOST}，目标 ${TARGET}"

# ── 交叉工具链（只在架构不一致时装）────────────────────────────────────
#
# 装的是 **宿主架构** 的 gcc 交叉包（如 arm64 机器上的 gcc-x86-64-linux-gnu）：
# gcc 本身原生执行，只是产出 x86_64 的目标文件。这正是绕开 qemu 模拟的关键。
if [ "$HOST" != "$WANT" ]; then
  GCC_PKG="${NEXTERM_GCC_CROSS_PKG:?跨架构构建必须提供 NEXTERM_GCC_CROSS_PKG（apt 包名，连字符写法）}"
  LIBC_PKG="${NEXTERM_LIBC_CROSS_PKG:?跨架构构建必须提供 NEXTERM_LIBC_CROSS_PKG}"
  echo "[bin] 装交叉工具链：${GCC_PKG} ${LIBC_PKG}"
  apt-get update -qq
  apt-get install -y -qq --no-install-recommends \
    "$GCC_PKG" \
    "$LIBC_PKG"
  "${NEXTERM_CROSS_PREFIX}-gcc" --version | head -1
else
  echo "[bin] 同架构，不需要交叉工具链（cc-rs 用系统 cc）"
fi

# ── 目标的标准库 ──────────────────────────────────────────────────────
#
# 同架构时 rust 镜像自带；跨架构要补装。`rustup target list --installed` 是幂等的判据。
if rustup target list --installed | grep -qx "$TARGET"; then
  echo "[bin] rust-std 已就位：${TARGET}"
else
  echo "[bin] 安装 rust-std：${TARGET}"
  rustup target add "$TARGET"
fi

cd /src
echo "[bin] cargo build --release --target ${TARGET}"
exec cargo build --release --locked --no-default-features --features server \
  --target "$TARGET" --bin nexterm-server
