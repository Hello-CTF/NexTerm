FROM --platform=linux/amd64 registry.lazycat.cloud/u0642568667/library/debian:07c67640965241be

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        libstdc++6 \
        ca-certificates=20250419~deb12u1 \
        openssh-client=1:9.2p1-2+deb12u10 \
        sshfs=3.7.3-1.2~deb12u1 \
        fuse3=3.14.0-4 \
        tzdata=2026c-0+deb12u1 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 nexterm \
    && useradd --uid 10001 --gid nexterm --no-create-home \
        --home-dir /lzcapp/var/nexterm --shell /usr/sbin/nologin nexterm \
    && install -d -m 0700 -o nexterm -g nexterm /lzcapp/var/nexterm

COPY nexterm-server /usr/local/bin/nexterm-server
COPY ai-runtime /usr/local/bin/ai-runtime
COPY nexterm-helper-linux-amd64 /usr/local/bin/nexterm-helper-linux-amd64
COPY nexterm-helper-linux-arm64 /usr/local/bin/nexterm-helper-linux-arm64
COPY nexterm-helper-linux-armv7 /usr/local/bin/nexterm-helper-linux-armv7
COPY nexterm-helper-linux-riscv64 /usr/local/bin/nexterm-helper-linux-riscv64
COPY nexterm-helper-linux-loong64 /usr/local/bin/nexterm-helper-linux-loong64
COPY nexterm-helper-darwin-amd64 /usr/local/bin/nexterm-helper-darwin-amd64
COPY nexterm-helper-darwin-arm64 /usr/local/bin/nexterm-helper-darwin-arm64
COPY nexterm-helper-windows-amd64.exe /usr/local/bin/nexterm-helper-windows-amd64.exe
COPY nexterm-helper-windows-arm64.exe /usr/local/bin/nexterm-helper-windows-arm64.exe
COPY entrypoint.sh /usr/local/bin/nexterm-server-entrypoint
RUN test -x /usr/local/bin/nexterm-server \
    && test -x /usr/local/bin/nexterm-helper-linux-amd64 \
    && test -x /usr/local/bin/nexterm-helper-linux-arm64 \
    && test -x /usr/local/bin/nexterm-helper-linux-armv7 \
    && test -x /usr/local/bin/nexterm-helper-linux-riscv64 \
    && test -x /usr/local/bin/nexterm-helper-linux-loong64 \
    && test -x /usr/local/bin/nexterm-helper-darwin-amd64 \
    && test -x /usr/local/bin/nexterm-helper-darwin-arm64 \
    && test -x /usr/local/bin/nexterm-helper-windows-amd64.exe \
    && test -x /usr/local/bin/nexterm-helper-windows-arm64.exe \
    && test -f /usr/local/bin/ai-runtime/runtime.json \
    && test -f /usr/local/bin/ai-runtime/libnexterm_privacy.so \
    && test -f /usr/local/bin/ai-runtime/libonnxruntime.so \
    && ldd /usr/local/bin/nexterm-server /usr/local/bin/ai-runtime/*.so > /tmp/nexterm-runtime-deps \
    && cat /tmp/nexterm-runtime-deps \
    && ! grep -q 'not found' /tmp/nexterm-runtime-deps \
    && rm /tmp/nexterm-runtime-deps \
    && test -x /usr/bin/setpriv \
    && test -x /usr/bin/findmnt \
    && test -x /usr/bin/awk \
    || { echo "FATAL: server/runtime dependencies, helpers and ownership-migration tools must be available" >&2; exit 1; } \
    && chmod 0755 /usr/local/bin/nexterm-server-entrypoint

ENV NEXTERM_LISTEN=0.0.0.0:8080 \
    NEXTERM_DATA_DIR=/lzcapp/var/nexterm \
    NEXTERM_WEB_ROOT=/lzcapp/pkg/content/web \
    HOME=/lzcapp/var/nexterm

WORKDIR /lzcapp/var/nexterm

EXPOSE 8080

USER root

ENTRYPOINT ["/usr/local/bin/nexterm-server-entrypoint"]
