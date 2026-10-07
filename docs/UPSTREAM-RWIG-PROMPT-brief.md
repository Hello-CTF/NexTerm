# 一页问题简报（可直接粘贴给朋友的 AI）

> 用途：把懒猫部署下暴露的问题压成一段自包含的提示词，问**朋友那边的 AI**。
> 关键约束：开头两段是「场景限定」，必须保留 —— 否则容易被读成「这个服务端有通用缺陷」。
> 完整版（含逐条复现命令、代码行号）见 `UPSTREAM-RWIG-ISSUE-origin-guard.md`。
> 剪裁建议见文末。

---

```text
背景：我们在把朋友的 NexTerm Go 版（分支 rwig @ ce84b62，v0.2.2-rc.1）部署到
「懒猫微服」这个平台上，页面白屏打不开。想请你帮我们确认定位对不对、修法是否妥当。

先说清楚前提，避免误解成服务端本身的问题：**这个现象只在「懒猫微服」这一种部署
形态下出现** —— 它的平台网关会反代并改写 Host。本地直接跑 nexterm-server
（127.0.0.1:8080）、或任何不改写 Host 的部署方式，一切正常。所以我们不认为这是
服务端自身的通用缺陷，而是「反代改写了 Host」这个前提被触发后才暴露出来的一个洞。

部署形态：懒猫微服 LPK v2，平台网关反代到容器内的 http://nexterm-server:8080。

现象（仅在这个部署下）：浏览器白屏；入口 JS/CSS、POST /rpc、/ws/* 全部 403，
响应体是 "origin is not allowed"。

我们的排查路径：
1. 静态资源和 API 一起 403，先怀疑是网关在拦。但在容器内直连
   http://nexterm-server:8080、带同样的请求头同样 403 ⇒ 确定是应用自己返回的，
   排除网关。
2. 接着二分请求头，发现唯一的触发条件就是「带 Origin 头」：
   同一路径同一端口，Origin=http://nexterm-server:8080 → 200；
   Origin=https://<公网域> → 403；不带 Origin → 200。
   中间那行是关键判据：Origin 与 Host 一致就放行 ⇒ 服务端只拿 r.Host 做同源判定。
3. 回到代码，链路就闭合了：server.go:142 把 transportGuard 挂在全部路由上
   （静态资源也走这层）；transport_security.go 的 requestOriginAllowed
   在白名单之外只有一条同源豁免 —— Origin 的 host 与 r.Host 相等。
   而反代把 Host 改写成了上游名（容器内实测为 nexterm-server:8080，客户端显式发
   Host 头也盖不住）⇒ 这条豁免永远命中不了。
4. 浏览器为什么一定会带 Origin：Vite 入口是 <script type="module" crossorigin>，
   会强制走 CORS 模式；同源 fetch 的 POST 也带。所以静态资源和 API 一起死，
   只改前端修不干净。
5. 之所以一直没被发现：仓库里那 126 行测试全跑在 localhost 上，那里 Host 与
   Origin 恰好一致、豁免天然命中 —— 只有放在反代后面才炸。

我们已实测：懒猫这个网关会下发 X-Forwarded-Host（只发 Origin、不发任何转发头时，
服务端仍能看到公网域作为候选 host）。

我们采用的修法（想请你判断是否合适）：
- requestOriginAllowed 的同源判定兼收 X-Forwarded-Host 与 RFC 7239 Forwarded 的 host；
- AllowedOrigins 字段原先没有任何配置入口（cmd/nexterm-server/main.go 没接线），
  补上 --allowed-origin / NEXTERM_ALLOWED_ORIGINS。
我们已在本地改完并重新部署验收过：静态资源 / RPC / WS 全部恢复，带恶意 Origin
仍被 403；三个新增回归测试在回退源码后确实失败。但不确定这是不是最合适的修法，
所以想先向你们确认。

想请你判断：
- 我们对「只在反代后面复现」这个前提的理解对不对？定位有没有错？
- 这条修法会不会引入新的信任问题（转发头客户端可伪造）？还是说静态资源
  本就不该进这层 guard？
- WebSocket / SSE / CORS 预检 这几条路径，有没有我们漏掉的同类问题？

另外一个独立的小发现（同一个分支，跟上面不是一回事）：
durable 终端硬依赖 tmux，缺失时 attach 直接返回 unsupported，而且刻意没有易失
降级（durable_production_test.go 显式断言「不得回退到 volatile」）。容器镜像里
默认不带 tmux ⇒ 「关掉网页不断会话」这个能力在容器化部署下默认失效。
这是有意设计还是遗漏？
```

---

## 剪裁建议

| 只要问 | 就删掉 |
|---|---|
| 只问 Origin 那条 | 最后「另外一个独立的小发现」段 |
| 想再短一截 | 第 4、5 点（合并成一句「浏览器必带 Origin、CI 全在 localhost 所以没发现」） |
| 已经不想听复现过程 | 第 1～2 点整段，只留第 3 点的代码链路 |

⚠️ **开头两段（背景 + 场景限定）不要删**：那是防止对方把问题理解成「服务端通用缺陷」的唯一防线。
