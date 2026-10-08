FROM --platform=linux/amd64 registry.lazycat.cloud/u0642568667/library/debian:07c67640965241be

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        openssh-client \
        sshfs \
        fuse3 \
        tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY nexterm-server /usr/local/bin/nexterm-server
COPY entrypoint.sh /usr/local/bin/nexterm-server-entrypoint
RUN test -x /usr/local/bin/nexterm-server \
    || { echo "FATAL: nexterm-server 没有执行位（构建上下文里的权限丢了？）" >&2; exit 1; } \
    && chmod 0755 /usr/local/bin/nexterm-server-entrypoint

ENV NEXTERM_LISTEN=0.0.0.0:8080 \
    NEXTERM_DATA_DIR=/lzcapp/var/nexterm \
    NEXTERM_WEB_ROOT=/lzcapp/pkg/content/web

WORKDIR /lzcapp/var

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/nexterm-server-entrypoint"]
