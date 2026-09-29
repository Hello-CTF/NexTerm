# 写入过程可见 + 先读后写门禁提前（2026-09-30）

> 触发：真机截图。AI 说「好的，这次做一次大改……我会用 `write_file` 完整重写」
> 之后界面静止，直到一张带着完整 60 行 diff 的确认卡片「啪」地弹出来。
>
> 用户的两句判断：
> 1. 「写入工作是在进行完后弹出是否允许的……如果量过大会让用户误以为卡死，
>    所以写入状态也要做流式展示」
> 2. 「门禁可能还会导致重写，浪费 token」

## 一、先分清哪句成立

| 用户的判断 | 核实结果 |
|---|---|
| 写入过程缺流式反馈，大内容时像卡死 | ✅ **成立**，能定位到具体的一行（§2.1） |
| 「写入是在进行完后才弹是否允许」 | ⚠️ **不成立，但观感是真的**。确认卡片一直都在工具**执行之前**；用户看到「内容已经完整出现」是因为**内容必须先生成完**（它就是 tool_call 的参数），卡片才可能带着 diff 弹出来 —— 协议决定的，不是顺序错了。真正缺的是「内容在长」这个信号（§2.1） |
| 门禁会导致重写、浪费 token | ✅ **成立**，且找到了确凿原因：`write_file` 的说明书漏写了这条规则（§2.3） |

所以要做的是：**把「内容在长」这个信号补出来**（第一条）+ **修掉门禁那处漏写**（第三条）。
第二条改了描述与信号之后，那个观感自然消失 —— 用户会看到
「正在准备写入内容 · 已生成 1.2 KB」，知道那是**在生成**，不是在写入。

---

## 二、核实：三条代码事实

### 2.1 生成 tool_call 参数的整段时间里，界面零事件

`ai/agent.rs` 的回调只认两种片段：

```rust
match item {
    StreamItem::Delta(t)     => { chan2.send(AiEvent::Delta { text: t }); }
    StreamItem::Reasoning(t) => { chan2.send(AiEvent::Reasoning { text: t }); }
}
```

而 `StreamItem` 就只有这两个变体。tool_call 的参数分片到了
`provider/openai_compat.rs` 里被 `entry.2.push_str(a)` **静默累积**，一个字都不往外说。

后果很具体：模型先 delta 出一段开场白（「好的，这次做一次大改…」，界面在动），
然后转入 tool_call 阶段 —— **界面从此完全静止**，直到参数攒齐、`ToolCall` 事件发出，
确认卡片才出现。写 60 行内容，这段静默可能几十秒。

> 注意 `delta` 分支里有 `setStatus(null)`（「开始吐字就说明不再思考中了」）。
> 所以开场白一出来，状态条还会主动让位 —— 静默期连那行字都没有。

### 2.2 门禁在**用户点允许之后**才检查

`read_gate` 挂在 `tools::execute` 里（`tools/mod.rs:169` 与 `:192`），
而 `tools::execute` 是在 `ConfirmDecision::Allow` 之后才被 `execute_with_diff` 调用的。

于是：用户对着一张带完整 diff 的卡片点了「允许」→ 才被告知「本次任务还没读过这个文件」。

### 2.3 `write_file` 的说明书漏写了「必须先读」—— 这是 token 浪费的根因

| 工具 | 门禁是否生效 | 说明书有没有写 |
|---|---|---|
| `edit_file` | ✅ 生效 | ✅ 写了：「改文件前必须先用 read_file 读过该文件。只适合改动局部；大范围重写用 write_file。」 |
| `write_file` | ✅ **同样生效** | ❌ **一个字都没提** |

模型看不见规则 ⇒ 直接 `write_file` ⇒ 把整份内容（可能上千 token）生成完
⇒ 参数齐了、确认卡片弹了、用户点了允许 ⇒ **门禁拒绝**
⇒ 模型 `read_file` + 把整份内容**再生成一遍**。

**白点一次点击，白烧一遍 token。** 用户说的「门禁导致重写」就是这个。

---

## 三、改动

| 文件 | 改动 |
|---|---|
| `ai/provider/openai_compat.rs` | 新增 `StreamItem::ToolArgs { name, chars }`，每个参数分片都往外报；顺手把内联的 SSE 行解析抽成纯函数 `handle_payload`，让它**可测** |
| `ai/mod.rs` | 新增 `AiEvent::ToolArgs { tool, chars }` |
| `ai/agent.rs` | 回调按 `TOOL_ARGS_PING`（120ms）节流转发；**把先读后写门禁提前到确认卡片之前**；`is_write_tool` 改为复用 `edit::WRITE_TOOLS` |
| `ai/takeover.rs` | 接管模式的回调同样转发（它也会生成工具参数，沉默在那里一样像死机） |
| `ai/tools/edit.rs` | 新增 `WRITE_TOOLS` 常量 + `is_write_tool` + `read_gate_for`（提前版入口）；工具名单收敛成一份 |
| `ai/tools/server.rs` | **补全 `write_file` 的说明书**：改已存在的文件前必须先 `read_file`、新建文件也要先探一次、只改几行优先用 `edit_file` |
| `src/features/ai/AiSidebar.tsx` | 新增 `StatusLine` 类型与 `case "toolArgs"`；`toolCall` 到达时清掉状态条；`statusText` 支持 `tool_args` 阶段（`preparingLabel` + `formatBytes`） |
| `src/demo/mock.ts` | 写文件演示场景补上「生成参数」这一段（间隔对齐 120ms 节流） |

### 两处「刻意不做」

- **执行阶段的字节进度**（点完允许之后「正在写入 N/M 字节」）没做。本地
  `tokio::fs::write` 是一次调用，写几 KB 是微秒级；真会慢的是 SSH/SFTP 传大文件，
  那要改 `FileSystem` trait 加分块写。**收益不明而改动面大**，留作后续。
- **让预览的读也登记 `mark_read`**（这样门禁就不会拒）没做。预览是**内核替模型**
  读的，模型的上下文里并没有文件内容 —— 登记了等于门禁形同虚设（模型可以永远不读、
  直接写）。门禁的语义是「模型自己别盲改」，不是「用户看不见」。

### 一处必须说清的期望管理

**提前门禁省不了 token。** 它省的是**用户白点一次点击 + 少一次往返**。
内容在门禁检查之前就已经生成完了（那是 tool_call 的参数），
那时候再拦，钱已经花了。

**能省 token 的只有 §2.3 那处说明书补全** —— 让模型一开始就知道要先读。
这一条改的是模型行为，效果要在真机上观察。

---

## 四、验收证据

### 4.1 反向验证：把三处改动分别改回旧行为，跑同一批用例

脚本带 `trap` 自动还原，事后两个文件的 sha256 与实验前**逐字相同**：

```
FIXED_SHA_OPENAI    = f85e167dd217a87a29301b25fdfa73fe4fcfa284a3d851ed956b2bfcc4b5a431
FIXED_SHA_AGENT     = 886b0e5af489dab2ea381685daa2e60dc25d56cc1b51fa010ea30350d8e587c2
RESTORED_SHA_OPENAI = f85e167dd217a87a29301b25fdfa73fe4fcfa284a3d851ed956b2bfcc4b5a431
RESTORED_SHA_AGENT  = 886b0e5af489dab2ea381685daa2e60dc25d56cc1b51fa010ea30350d8e587c2
```

| 用例 | 旧行为下 | 守的是什么 |
|---|---|---|
| `provider::…::tool_args_are_reported_on_every_arguments_fragment` | ❌ **FAILED**<br>`left: 0, right: 4` —— 一条进度都没报 | 每个参数分片都要往外报 |
| `agent::tests::read_gate_runs_before_the_confirm_card` | ❌ **FAILED** | 门禁必须早于确认卡片 |
| `agent::tests::tool_args_progress_is_forwarded_to_the_frontend` | ❌ **FAILED** | 回调要把进度转成事件 |
| `provider::…::plain_text_stream_reports_no_tool_args` | ✅ ok | 纯文本流不许混进非正文事件 |
| `edit::tests::t09_read_gate_blocks_until_marked` | ✅ ok | 门禁**判定本身**没被改（没有顺手放宽） |
| `edit::tests::t11_read_gate_for_only_covers_write_tools` | ✅ ok | 非写文件类工具不被门禁拦 |

> **如实标注一处强度**：`read_gate_runs_before_the_confirm_card` 第一次反向验证
> 红的机制是「符号被替换掉、找不到」，**不是**「顺序被反过来」。
> 为此把顺序判断抽成 `gate_precedes_confirm`，另补 4 条**构造文本**断言
> （顺序反 / 只缺左 / 只缺右 / 全空）—— 让「顺序」本身也有跑得出来的证据，
> 而不是只证明两个符号都在文件里。

### 4.2 单测

| 项 | 结果 |
|---|---|
| `cargo test -p nexterm --lib` | **199 passed; 0 failed**（本轮 +6） |
| `cargo test --workspace` | 199 + 2 + 1 + 0 passed，**0 failed**（含 `local_pty`、`ssh_e2e` 集成测试） |

本轮新增 6 条：

| 用例 | 位置 |
|---|---|
| `tool_args_are_reported_on_every_arguments_fragment` | `provider/openai_compat` |
| `plain_text_stream_reports_no_tool_args` | `provider/openai_compat` |
| `t11_read_gate_for_only_covers_write_tools` | `tools/edit` |
| `t12_write_tool_list_has_one_source_of_truth` | `tools/edit` |
| `read_gate_runs_before_the_confirm_card` | `ai/agent` |
| `tool_args_progress_is_forwarded_to_the_frontend` | `ai/agent` |

### 4.3 真 UI（演示模式 `?demo=1`，浏览器连拍三帧）

状态条**确实在动**，不是静态文案：

| 帧 | 看到什么 |
|---|---|
| `29-ai-write-progress-45b.png` | `正在准备写入内容 · 已生成 45 字节` |
| `30-ai-write-progress-89b.png` | `正在准备写入内容 · 已生成 89 字节` |
| `31-ai-confirm-after-progress.png` | 状态条**已让位**，确认卡片接管（含完整 diff） |

之后点「允许一次」→ 出现「文件已修改」变更记录卡（**上一轮的功能没被改坏**），
且状态条不再出现。

⚠️ 这三张走的是**演示模式**（前端 + mock 事件）。它证明的是「前端收到该形状事件
时怎么渲染」；「内核真的会推这类事件」由 `handle_payload` 的单测覆盖。
**不等于真机端到端。**

### 4.4 静态门

| 项 | 结果 |
|---|---|
| `cargo fmt --all -- --check` | exit 0 |
| `cargo clippy --all-targets --all-features -- -D warnings` | exit 0 |
| `pnpm typecheck` | exit 0 |
| `pnpm lint`（`--max-warnings 0`） | exit 0 |

---

## 五、遗留 / 未做

- **真机端到端仍未跑通**：本机 `127.0.0.1:8888` 的本地模型端点没在运行，
  起不了「让 AI 写文件 → 弹确认 → 点允许 → 落盘」这条完整链路。
  证据强度到**逻辑级 + 单测级 + 演示模式 UI 级**，不宣称端到端闭环。
- **演示模式截图 ≠ 真机**：截图证明的是「前端收到该形状事件时怎么渲染」，
  以及「内核真的会推这类事件」（由 `handle_payload` 的单测覆盖）。
- **`chars` 是字节数不是字符数**：`String::len()`。刻意不数 `chars()` ——
  那是 O(n)，在逐 token 累积的循环里会退化成 O(n²)。UI 上也按字节展示。
- 承接上一轮的遗留：`fileChange` 不落库、`exec` 改文件（`sed -i` / `mv` / `rm`）
  不产生 diff、前端零测试框架（所以这次的 UI 断言只能靠一次性探针 + 截图）。
- 门禁拒绝时，模型仍要重发一遍内容（协议上无法复用已消费的 tool_call）。
  说明书补全降低了它发生的概率，但没有消除。
