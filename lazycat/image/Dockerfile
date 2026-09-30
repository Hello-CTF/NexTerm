# NexTerm 服务端镜像（懒猫微服 LPK，Linux/amd64）。
#
# # 为什么用 debian 而不是 alpine
#
# 二进制是在 `rust:1-bookworm` 里编的，glibc 链接 ⇒ alpine（musl）跑不了。
# 要上 alpine 就得编 musl 静态目标，那是另一条工具链，收益只有几十 MB，
# 不值得在第一次上架时引入。
#
# ⚠️ 基线镜像必须写成**懒猫官方源**的引用，不能写 `debian:bookworm-slim`。
#
# 这是 `upstream-match` 机制的前提（`spec/build.md` §五：构建器**沿父镜像链按前缀
# 匹配上游**，默认前缀 `registry.lazycat.cloud`）。匹配成功 ⇒ 只内嵌我们自己的层
# （LPK ≈ 16 MiB）；匹配不上 ⇒ **整个基线层全量内嵌**（LPK ≈ 43 MiB，实测值）。
#
# 官方源里的引用由这一步产出（一次性，换基线版本时重跑）：
#
#     lzc-cli appstore copy-image debian:bookworm-slim --arch amd64
#     → registry.lazycat.cloud/<开发者用户名>/library/debian:<IMAGE_ID>
#
# 官方发布文档明确要求「上传完毕后需要手动调整引用」，否则审核方可能装不上。
#
# ⛔ **不要试图在开发机上 `docker pull` 这个引用** —— 会 401：
#      Error response from daemon: ... no basic auth credentials
#    registry 的挑战是 `www-authenticate: Basic realm="Registry Realm"`，
#    而社区账号（手机号 / username / 商店 token，三种组合都试过）**一律 401**。
#    官方文档写明「registry.lazycat.cloud 仅供微服内部使用」，社区笔记也印证
#    「就一个加了认证的 registry，只是微服有凭证可以直接进」。
#    ⇒ 所以基线镜像**只能在盒子侧解析**，这也是 `lzc-build.yml` 用 `builder: remote`
#      的原因（见该文件注释）。本机 `builder: local` 路线下这条匹配必然失败。
#
# 为什么还要 `--platform`：`images.<alias>` **没有** platform / arch 字段
# （`spec/build.md` §五），不写就按构建机架构出镜像。盒子本身是 amd64，写死更稳。
# 架构要与 `image/build-server.sh` 的 `LZC_ARCH` **手动保持一致**（后者管二进制）。
FROM --platform=linux/amd64 registry.lazycat.cloud/u0642568667/library/debian:07c67640965241be

# openssh-client：NexTerm 的核心能力（ssh / sftp / 端口转发都靠它）
# sshfs + fuse3  ：磁盘挂载。平台声明 fuse.mount 后会注入 /lzcinit/fusermount3 并加进 PATH，
#                  官方明确说标准 FUSE 客户端开箱可用 —— 我们只需要装着 sshfs 本体。
# tzdata         ：容器里时间要对得上（会话列表、审计时间是按本地时区给用户看的）
# ca-certificates：AI 提供方是 https 的（rustls 用自己的根证书库，但有些工具链仍会读系统库）
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        openssh-client \
        sshfs \
        fuse3 \
        tzdata \
    && rm -rf /var/lib/apt/lists/*

# 二进制由 buildscript（image/build-server.sh）编好后拷进来。
#
# ⛔ **这里不要再跟一条 `RUN chmod 0755`** —— 实测它会让二进制被打包两遍：
# 构建上下文里的文件已经是 0755（build-server.sh 用 `install -m 0755`），
# `COPY` 会原样保留权限位；而入 RUN 之后 overlayfs 为了记录*元数据*变更会
# **把整个文件复制进新层**。实测两个层内容逐字节相同（都是 16337808 字节的
# `usr/local/bin/nexterm-server`），白占 7.14 MiB —— LPK 22.77 MiB → 15.96 MiB。
#
# 权限改为在**构建期**断言：丢了执行位就是打包失败，而不是等用户装完启动才报错。
# 这条 RUN 不产生数据层（空 diff），可以忽略其体积。
COPY nexterm-server /usr/local/bin/nexterm-server
RUN test -x /usr/local/bin/nexterm-server \
    || { echo "FATAL: nexterm-server 没有执行位（构建上下文里的权限丢了？）" >&2; exit 1; }

ENV NEXTERM_LISTEN=0.0.0.0:8080 \
    NEXTERM_DATA_DIR=/lzcapp/var/nexterm \
    NEXTERM_WEB_ROOT=/lzcapp/pkg/content/web \
    RUST_LOG=info

# /lzcapp/var 是持久卷挂载点，一定存在；不用 /lzcapp/var/nexterm 是因为它由
# 应用自己创建，作为 WORKDIR 会要求镜像构建期就存在。
WORKDIR /lzcapp/var

EXPOSE 8080

# 健康检查交给 manifest 的 `health_check.test_url`（平台侧做，不需要容器里有 curl）。
ENTRYPOINT ["/usr/local/bin/nexterm-server"]
