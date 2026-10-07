# [bug] `transportGuard` 在反向代理后面会把入口 JS/CSS 与 `/rpc` 一起 403（Host 被改写 ⇒ 同源豁免失效）

> 直接可粘贴的 issue 正文。环境、复现、根因、修复建议都在下面。
> 实测日期 2026-10-04 ｜ 首测版本 `rwig` @ `ce84b62`（v0.2.2-rc.1）｜ Go 1.26.8 ｜ 部署形态：懒猫微服 LPK v2（`/=http://nexterm-server:8080/`）
>
> ⚠️ **下面这段是「给上游看的」原文，本仓已经先修好了**（`local/lazycat-go-dev` @ `ca7a713`，
> 走的是「A：同源判定兼收 `X-Forwarded-Host`」+「C：`--allowed-origin`/`NEXTERM_ALLOWED_ORIGINS`」）。
> 原先标注为「未验证」的那条未知量——懒猫网关发不发 `X-Forwarded-Host`——**已实测：发**，证据在本文件
> “关键未知量已有答案”一节。修复后的实机验收表在 `UPSTREAM-RWIG-GO-BRANCH-REVIEW.md` §5.3。
>
> ### 🔁 2026-10-05 复测：缺陷在 v0.2.2-rc.2（`b4df788`）上**依然存在**
>
> - `internal/server/transport_security.go` 在 rc.1 → rc.2 之间**逐字节未变**
>   （`git diff ce84b62..b4df788 --stat` 里根本没有这个文件）；
> - 在 rc.2 的真实部署上重新采到了同一条 WARN 日志
>   （`path=/assets/index-CALkHFvn.js`，即 rc.2 的入口 JS）⇒ 网关行为未变、缺陷未修；
> - 上游至今**没有**为 `AllowedOrigins` 提供任何 CLI flag 或环境变量入口
>   （`git grep "allowed-origin\|ALLOWED_ORIGIN" origin/rwig` → 空）。
>
> ⇒ 结论不变，且**纯上游推到懒猫 100% 白屏**。完整逐项判定（含 tmux 与 migration 两项）
> 见 **`UPSTREAM-RWIG-LAZYCAT-PATCH-AUDIT.md`**。

---

## 现象

把 server 放在**反向代理**后面时，页面**白屏**，且 `/rpc` 全部失败。浏览器控制台：

```
Failed to load resource: the server responded with a status of 403 ()
  https://<host>/assets/index-DDzVmQ83.js
Refused to apply style from 'https://<host>/assets/index-DxcAilpj.css'
  because its MIME type ('text/plain') is not a supported stylesheet MIME type
```

`document.getElementById('root').children.length === 0` —— 入口模块从未执行。

403 的响应体只有一句：`origin is not allowed`（22 字节，`text/plain`）。

**注意这不是"只有静态资源"**：`POST /rpc` 同样 403。也就是说，即使把前端塞进去，功能也是死的。

对照：同一台机器上直接用 `http://localhost:PORT` 访问**完全正常**。

---

## 最小复现（不需要真反代，两条 curl）

```bash
# 起 server
nexterm-server --listen 127.0.0.1:8080 --web-root ./web

# ① 反代把 Host 改写成后端地址（nginx 默认 `proxy_set_header Host $proxy_host`、
#    docker compose 服务名、K8s Service 名，都是这个行为）
curl -i -H 'Origin: https://app.example.com' -H 'Host: nexterm-server:8080' \
     http://127.0.0.1:8080/assets/index-DDzVmQ83.js
# → HTTP/1.1 403 Forbidden
#   origin is not allowed

# ② 去掉 Origin —— 同一路径立刻正常（证明唯一变量就是 Origin）
curl -o /dev/null -w '%{http_code}\n' -H 'Host: nexterm-server:8080' \
     http://127.0.0.1:8080/assets/index-DDzVmQ83.js
# → 200
```

`POST /rpc` 同理（浏览器同源 `fetch` 的 POST **也会带 `Origin`**）：

```bash
curl -o /dev/null -w '%{http_code}\n' \
  -H 'Origin: https://app.example.com' -H 'Host: nexterm-server:8080' \
  -H 'Content-Type: application/json' -d '{"cmd":"forward_env"}' \
  http://127.0.0.1:8080/rpc
# → 403     （不带 Origin 时 → 200 + 真实业务 JSON）
```

### 我这边实跑到的结果

同一台机器、同一个端口，**只改 `Host` / `Origin` 两个头**：

| `Host` | `Origin` | 结果 |
|---|---|---|
| `nexterm-server:8080` | `https://app.example` | **403 `origin is not allowed`** |
| `nexterm-server:8080` | `http://nexterm-server:8080` | **200** |
| `nexterm-server:8080` | （不带） | **200** |

（这一组是在 server 所在容器的同网内、用 bash 的 `/dev/tcp` 手工发 HTTP 报文测的，刻意避开 curl / busybox 在 header 处理上的差异。第 2 行是关键判据：`Origin` 与 `Host` 一致就放行 ⇒ 证明**服务端只认 `Host` 头**。所以反代一旦把它改成后端地址，浏览器的同源请求就必然被判成跨源。）

**为什么浏览器一定会带 `Origin`**：Vite 产物给入口标签加了 `crossorigin`，这会强制请求走 CORS 模式：

```html
<script type="module" crossorigin src="/assets/index-xxx.js"></script>
<link rel="stylesheet" crossorigin href="/assets/index-xxx.css">
```

于是"反代改写 Host" + "浏览器带 Origin" 两条一叠加，**同源请求被判成跨源**。

---

## 根因

三处代码合起来构成这个洞：

| # | 位置 | 说明 |
|---|---|---|
| 1 | `internal/server/server.go:142` | `s.handler = s.transportGuard(s.routes(config))` —— guard 包住**全部路由，含静态资源与 SPA 回退** |
| 2 | `internal/server/transport_security.go:17-22` | 只要 `Origin != ""` 且 `!requestOriginAllowed(...)`，直接 `403 origin is not allowed` |
| 3 | `internal/server/transport_security.go:60` | 唯一的"同源豁免"是 `strings.EqualFold(parsed.Host, r.Host)` |

第 3 条在**直连**时是对的，在**反代后面必然失效**：反代会改写 `r.Host`，于是"同源"永远匹配不上。

本次实测的判据（说明 `r.Host` 确实被改写成了后端服务名）：

| 请求 | 结果 |
|---|---|
| `Origin: http://nexterm-server:8080` | **200** |
| `Origin: https://nextermgo.lazycore.heiyu.space`（就是页面自己的域名） | **403** |
| 不带 `Origin` | 200 |

`nexterm-server:8080` 并不在默认白名单里（`transport_security.go:13` 只有 `localhost:*` / `127.0.0.1:*`），却能通过 ⇒ 它只能走"同源"那条分支 ⇒ `r.Host == "nexterm-server:8080"`，即反代把 Host 换成了后端服务名。

**已排除"是网关干的"**：在 server 所在容器的同网内直连短服务名，带 `Origin` **同样 403**，说明是应用自己返回的，与网关无关。

---

## 为什么现有的测试与 CI 抓不到

`internal/server/transport_security_test.go` 有 126 行，覆盖得挺细，但**全部在 `localhost` 上跑**。而 `localhost:PORT` 直连时 `Origin` 与 `r.Host` 恰好一致 ⇒ 第 3 条豁免命中 ⇒ 全绿。

「Host 被上游反代改写」这一维没有被覆盖，所以这个洞只在真实部署拓扑下才暴露。

---

## 另一个独立的小缺口

`cmd/nexterm-server/main.go` 构造 `server.Options` 时**完全没有传 `AllowedOrigins`**（只传了 `Listen` / `DataDir` / `WebRoot` / `SyncOnly`），而它也没有任何 flag 或环境变量入口。结果就是：**默认白名单永远只有 `localhost:*` / `127.0.0.1:*`，部署方想加也加不了。**

即便修了同源判定，反代场景下想显式放行公网域也没有手段。

---

## 修复建议

按取舍排序，可以组合：

**A. 同源判定兼收转发头**（最干净）

`requestOriginAllowed` 里除了 `r.Host`，再比对 `X-Forwarded-Host` 与 `Forwarded` 里的 host（`Forwarded` 要按 RFC 7239 解 `host=`）。注意这两个头都是客户端可伪造的 —— 但它们的可信度与 `r.Host` 同级（都来自反代），所以这里不引入新的信任问题（浏览器也无法在跨源请求上带自定义头而不触发预检，而预检本身带的是恶意 `Origin`，会被先拦掉）。

三种方案的效果与代价汇总：

| 方案 | 效果 | 代价 / 前提 |
|---|---|---|
| **A. 同源判定兼收转发头** | 最干净：不引入任何配置，换域名/换盒子自动跟着走 | 依赖反代是否下发该头 —— **已实测：懒猫网关下发**（见下） |
| **B. 静态资源不参与 Origin 校验** | 零依赖，单独就能修白屏 | 修不了 `/rpc`，功能仍是死的 |
| **C. 给 `AllowedOrigins` 开配置入口** | 部署方能显式声明公网域 | 单改不够（静态资源也走 guard）；且要求部署方知道自己的域名 |

### 关键未知量已有答案：懒猫网关**下发** `X-Forwarded-Host`

先在服务端加了一条 `WARN` 日志，把 `r.Host` 与 `X-Forwarded-Host` / `Forwarded` 里解析出的候选 host 一起打出来，然后发一条**只带 `Origin`、不带任何转发头**的公网请求：

```bash
curl -H "Lzc-Auth-Token: $TOK" -H 'Origin: https://evil.example' \
     https://nextermgo.lazycore.heiyu.space/assets/index-DDzVmQ83.js     # → 403
```

服务端日志（`/lzcapp/var/nexterm/logs/nexterm.log`）：

```
level=WARN msg="rejected request origin" origin=https://evil.example
  host_candidates=nexterm-server:8080,nextermgo.lazycore.heiyu.space
  path=/assets/index-DDzVmQ83.js
```

第二个候选 host（`nextermgo.lazycore.heiyu.space`）**只可能来自网关**，客户端那条 curl 没发它。
⇒ 网关把原始 Host 改写成上游地址（第一个候选），同时用 `X-Forwarded-Host` 保留了对外域名。

**所以 A 是充分解**：只改同源判定、不加任何配置，这一台和别人的盒子都会自动好。
（我这边的落地同时做了 A + C 作为双保险：C 用部署期渲染的 `{{ .S.AppDomain }}`，不写死域名。）

**注意事项**：

- 单改 C **不够** —— 静态资源也走 guard，仍然会白屏；
- 单改前端去掉 `crossorigin` **也不够** —— `/rpc` 与 `/ws/*` 仍然是 403；
- 最小可用组合是 **B + C**；最干净的是 **A**，实测可行，**推荐直接走 A**。
- `Forwarded`（RFC 7239）我没能在懒猫上实测（网关只发了 `X-Forwarded-Host`），但按规范一起支持能覆盖 nginx/K8s 那类只发它的反代。

---

## 影响面

- 只在**会改写 `Host` 的反向代理**后面出现。nginx 默认配置、docker compose 服务名、K8s Service、以及本次的懒猫微服都算。
- 上游 CI 与本地开发全在 `localhost`，因此长期绿着。
- 该提交已在 `origin/rwig` 上 ⇒ **release v0.2.2-rc.1 同样含此缺陷**。
- 不确定是否影响 Wails 桌面版（桌面走 IPC 而非 HTTP，我没验证，不下结论）。
