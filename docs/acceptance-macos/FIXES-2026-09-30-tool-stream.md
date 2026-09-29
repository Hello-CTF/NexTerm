# 工具参数被上游整块缓冲：为什么上一轮的进度条根本没亮（2026-09-30）

> 触发：装机后用户截图追问 ——「**似乎当前运行的并没有 你帮我替换了么**」。
>
> 截图里：AI 输出完 reasoning（「已确认当前文件内容（4 行），现在一次性重写
> 为超过 50 行的内容」）之后界面**静止几十秒**，然后直接弹出带完整 60 行
> diff 的确认卡片 —— 也就是说，上一轮刚加的「正在准备写入内容 · 已生成 N 字节」
> **一次都没出现**。

## 一、结论先说

| 待核实的问题 | 核实结果 |
|---|---|
| 装机版替换成功了吗 | ✅ **成功**。运行实例启动于 01:12:40，二进制落盘于 01:12:31；且新二进制内嵌着新前端资源名（§3.1） |
| 那为什么进度条不亮 | ❌ **上一轮的修法建立在一个错误前提上**：它假设 provider 会**逐分片**下发 `tool_call.arguments`。实测智谱**默认整块下发** —— 全程只有 1 个分片，进度条只可能闪一帧，紧接着被 `toolCall` 清掉，肉眼不可见（§2） |
| 能修吗 | ✅ **能**，而且是根治：请求体加厂商扩展 `tool_stream: true`，同一请求从 **1 片**变 **494 片**（§2.2） |

上一轮那条 `StreamItem::ToolArgs` **没有白写**（对本来就分片下发的厂商直接生效），
但它**单独不足以**解决问题 —— 这是本轮补上的那一半。

---

## 二、实测：上游到底怎么下发工具参数

本机跑的就是真配置，直接从应用自己的库里取端点，不经应用、直连复现。

```bash
# 端点/模型/key 从应用自己的 setting 表读，不猜
sqlite3 -readonly "$HOME/Library/Application Support/NexTerm/data.db" \
  "select json_extract(value,'\$.apiKey') from setting where key='ai.provider';"
# baseUrl = https://open.bigmodel.cn/api/paas/v4   model = glm-5.3-flash
```

探针发一份与用户截图同形的请求（「把这份 60 行内容一次写入某路径」+ `write_file`
工具声明 + `stream: true`），逐行读 SSE，只数 `delta.tool_calls[].function.arguments`
出现了几次：

### 2.1 默认行为：整块

```
HTTP 200 text/event-stream
SSE data 行数: 40
delta 字段出现次数: {'role': 39, 'reasoning_content': 38, 'tool_calls': 1, 'content': 1}
arguments 分片数: 1
  片 0: id=call_0345…, name=write_file, arguments 长度 = 1963 字节
=> 单块到达
```

**`tool_calls` 全程只出现 1 次。** 也就是说：模型在服务端把整份文件内容
（1963 字节）**生成完**，才朝客户端吐第一个字节。客户端在这几十秒里
**物理上收不到任何东西** —— 无论解析层怎么写，都变不出进度来。

`reasoning_content` 是分片的（38 次），所以「思考」阶段界面在动 —— 这正好
解释了截图里的观感：**思考时在动，思考一结束就死**。

### 2.2 加 `tool_stream: true`：逐 token

同一个请求，只多一个字段：

```
HTTP 200
delta 字段出现次数: {'role': 33, 'reasoning_content': 32, 'tool_calls': 494, 'content': 1}
arguments 分片数: 494
  前 10 片长度: [2, 7, 3, 4, 1, 2, 3, 4, 11, 5]
=> 分片有效
```

聚合校验：`index` 恒为 0，**只有第 1 片带 `id` + `name`**（`args_len=2`），
后续 493 片只带增量；把 494 片拼起来 `serde_json` 能解析，`content` **恰好 61 行**、
`path` 正确 —— 形状与默认路径**完全一致**，只是切法变了。

> 这一点很关键：现有的 `pending_tools: HashMap<u64,(id,name,args)>` 聚合逻辑
> **不用改**，它对两种切法都成立。

---

## 三、核实装机（回答「你帮我替换了么」）

### 3.1 替换确实成功了 —— 但有两次「看起来像没成功」的坑

**坑一：进程启动时间必须早于二进制落盘才叫没换。**

```
装机二进制 mtime : 09-30 01:12:31
运行实例启动时间 : 09-30 01:12:40   （从应用日志 UTC 17:12:40Z 换算）
```

进程比文件**晚 9 秒**启动 ⇒ 它映射的就是新文件。`lsof` 佐证：pid 90075 的
`txt` 段指向 `/Applications/NexTerm.app/Contents/MacOS/nexterm`，
inode/size 与当前装机文件一致；若进程更早启动，它会映射旧 inode，
路径会显示成被 `mv` 走的备份名。

**坑二：不要用「前端资源文件名」判断，除非前端真的改了。**

| 二进制 | `index-AE7tHTD2.js` | `mock-BnJpVAeo.js` | `index-tsTpuSFr.css` |
|---|---|---|---|
| 上一轮装机版 | 1 | 1 | 1 |
| 它的前身（备份） | **0** | **0** | 1 |

上一轮能这么判（JS 资源名从旧的换成了 `AE7tHTD2`），是因为**前端确实改了**。
本轮只动 Rust，前端产物哈希不变 ⇒ **这个判别物本轮失效**。
所以本轮换了判别物：新代码里的字面量 `tool_stream`。

```
新产物   : 1
旧装机版 : 0
最旧备份 : 0
```

**教训**：装机判据要选「本次改动里必然出现的新符号」，不要固定用一个。
选错了会得到「换了 = 没换」的假结论。

---

## 四、改动

| 文件 | 改动 |
|---|---|
| `ai/provider/openai_compat.rs` | 新增 `wants_tool_stream(base_url)` + `host_of(url)`；把请求体构造抽成 `stream_body()`；智谱系端点且**本轮带工具**时写入 `"tool_stream": true` |

```rust
if !tools.is_empty() {
    body["tools"] = /* … */;
    // 没有工具的一轮不需要它，也省得往纯聊天请求里塞厂商私货
    if wants_tool_stream(&self.cfg.base_url) {
        body["tool_stream"] = serde_json::Value::Bool(true);
    }
}
```

### 4.1 为什么不一视同仁地加

`tool_stream` **不是 OpenAI 协议字段**。OpenAI / DeepSeek 本身就逐分片下发，
凭空多一个未知字段有被 400 挡掉的风险 —— 而这里加错的代价是**所有对话都用不了**。
所以按 host 严格限定：

```rust
let host = host_of(base_url);   // 取 host，不含端口，小写
host.contains("bigmodel.cn") || host.contains("zhipuai") || host == "z.ai" || host.ends_with(".z.ai")
```

`host_of` 刻意**先剥出 host 再 `contains`**：直接对整串判断的话，
`https://my-gateway.example.com/proxy/bigmodel.cn/v1` 这种**把关键字写在路径里**
的地址会被误判成智谱，然后带着它不认识的字段去吃 400。

### 4.2 为什么把请求体构造抽成 `stream_body()`

只测 `wants_tool_stream` 这个布尔判断，**证明不了它被用上** —— 判断对了、
但忘了赋值，测试照样绿、线上照样卡。抽成独立方法后可以直接把产物摊开断言。

---

## 五、验收证据

### 5.1 反向验证：两个探针，红的机制各不相同

脚本带 `trap` 自动还原；文件 sha256 与实验前**逐字相同**
（`8ab206b448f2762f38ead403a1a99d051f1cb8b551e3a0a03aff6ea1de953169`）。

| 探针 | 模拟的失误 | 结果 |
|---|---|---|
| A：`Bool(true)` → `Bool(false)` | 判断对了，但没真正写进 body | `stream_body_carries_…` ❌ FAILED；其余 6 条 ok |
| B：`contains("bigmodel.cn")` → `contains("__never__")` | 整条规则漏了 | 两条新用例 ❌ FAILED；其余 5 条 ok |

探针 A 只红**一条**，正是想要的证据：说明「判定」与「写进 body」是两条独立的
断言，不是一条测试的两个别名。

### 5.2 单测

| 项 | 结果 |
|---|---|
| `cargo test -p nexterm --lib` | **201 passed; 0 failed**（本轮 +2） |
| `cargo test --workspace` | 201 + 2 + 1 + 0 passed，**0 failed** |

本轮新增：

| 用例 | 守的是什么 |
|---|---|
| `tool_stream_is_asked_for_only_where_it_exists` | 6 个该带的端点 + 4 个不该带的（含 `127.0.0.1` 自建、未知自建网关）+ **路径里含关键字不许误判** |
| `stream_body_carries_tool_stream_only_for_zhipu_and_only_with_tools` | 智谱带工具=true、DeepSeek 带工具=**没有该字段**、智谱无工具=**没有该字段** |

### 5.3 静态门

| 项 | 结果 |
|---|---|
| `cargo fmt --all --check` | exit 0 |
| `cargo clippy --all-targets --all-features -- -D warnings` | exit 0 |
| `pnpm typecheck` / `pnpm lint --max-warnings 0` | exit 0（本轮未改前端，跑一遍确认没被牵连） |

### 5.4 装机

| 项 | 值 |
|---|---|
| 构建 | `node scripts/build.mjs release`，1m 04s，内嵌前端 ✓，产物 23.9 MB |
| 装机二进制 mtime | 09-30 01:24:13 |
| 新 UUID | `B365FAF6-27BD-33A4-B98A-70414278695B` |
| 旧版本 | 备份为 `backup/nexterm-F0EB94C9-2D3D-35B6-B6EF-6B978F45C005.bin` |
| 签名 | `adhoc` |
| 运行实例 | pid **91977**，启动于 01:24:26（晚于落盘） |
| 今日日志 ERROR | **0** |

> 重启会丢终端标签页（无 session 持久化表），但 **AI 会话是落库的**
> （`ai_conversation` 6 条 / `ai_message` 16 条），刷新后仍在。

---

## 六、遗留 / 边界

- **端到端仍未在真机 UI 上观测到进度条动起来。** 本轮把「上游会分片」这一步
  从「假设」变成了「实测」，但「装机版在真界面里跑一次、看到数字在涨」还差
  用户点一次。留给用户的复验方法是：在 NexTerm 里对任意会话发一句
  「把 <某文件> 改写成 60 行」，观察是否出现「正在准备写入内容 · 已生成 N 字节」。
- **仍未做的兜底：provider 无关的等待心跳。** 现在这套只在 provider 真的分片时
  才有效。若换到某个同样会缓冲、又没有 `tool_stream` 开关的网关，界面照样会静默。
  可行的兜底是**前端**在收到任何流式事件后起一个 500ms tick，显示
  「模型生成中 · 已等待 N s」，直到 `toolCall` / 结束。本轮**没做**，因为它增加
  一条独立于真实进度的假信号，属于产品取舍，等用户拍板。
- **降级路径仍然全程静默**：`chat_streaming` 在流式失败时会退回 `chat_block`，
  整块请求天然零事件。这是既有行为，本轮未动。
- 承接上一轮的遗留不变：`fileChange` 不落库、`exec` 改文件不产生 diff、
  前端零测试框架、执行阶段没有字节进度。
