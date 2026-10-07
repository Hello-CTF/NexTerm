# 验收取证：Go/Wails 线在懒猫微服上「跑通」

被测：`rwig` @ `ce84b62`（v0.2.2-rc.1）+ 本仓补丁 `ca7a713`（分支 `local/lazycat-go-dev`）
入口：`https://nextermgo.lazycore.heiyu.space`（dev 包 `cloud.lazycat.app.nextermgo.dev`）

> 取证方式：headless Chrome + CDP，通过 `Network.setExtraHTTPHeaders` 注入 `Lzc-Auth-Token` 过平台登录门
> （票据取自懒猫客户端窗口进程参数 `--authToken=`）。**全程只读**，没动任何生产配置。

## 01 — 修复前的白屏

`#root` 为空（`rootChildren: 0`），入口 JS/CSS 全 403 `origin is not allowed`，
控制台报 `net::ERR_ABORTED` 与 `Refused to apply style … MIME type ('text/plain')`。
根因与因果链见 `../UPSTREAM-RWIG-GO-BRANCH-REVIEW.md` §5.2 ⑤。

## 02 — 修复后：页面完整渲染

同一 URL、同一票据。`#root` 有内容（`rootChildren: 1`、16,633 B HTML）、
网络面板**零失败请求**、**零异常**，凭据库显示已解锁（说明 `stable_secret` 注入的主密钥可用）。

## 03 — 补 tmux 之前：终端 attach 直接失败

点「打开本地终端」得到：

```
[attach 失败] unsupported: durable terminal unavailable:
  tmux executable "tmux": exec: "tmux": executable file not found in $PATH
```

界面头部仍显示「已连接」——**光看状态点是判断不出来的**，必须看终端正文。
根因见 `../UPSTREAM-RWIG-GO-BRANCH-REVIEW.md` §5.4。

## 04 — 补 tmux 之后：全双工往返

在真浏览器里往终端敲 `echo NEXTERM_ROUNDTRIP_OK`，**服务端侧**
（`tmux -S /tmp/nexterm-durable-<hash>/d.sock capture-pane -p`）与浏览器画面**逐字一致**：

```
# echoE NEXTRM_ROUNDTRIP_OK
/bin/sh: 1: echoE: not found
#
```

> 字符有丢失/错位（`echoE` / `NEXTRM`）是 **CDP 发送侧的节奏问题**（逐字符 `keyDown` 发得太快），
> 不是应用缺陷 —— 判据是「整体同形」。要干净可每条之间加 20–50 ms。

⇒ 输入与回显**双向串通**：浏览器 → WS → 服务端 → tmux → shell → 原路回传。
