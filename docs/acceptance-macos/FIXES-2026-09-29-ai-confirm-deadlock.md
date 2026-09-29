# NexTerm 缺陷修复记录 · AI 确认链自死锁（2026-09-29 夜）

> 来源：实机反馈（本地终端会话）
>
> 1. 「写文件会有问题 —— 工具卡片 `write_file 写入 /Users/macmini/Downloads…` 一直转圈，文件没落盘」
> 2. 「停止按钮点完后也不会停止，卡片处于转圈状态；再继续输入就没有回复了，会卡住」
>
> 结论：**三条症状是同一处自死锁的三个表现面**。下面按「症状 → 证据 → 根因 → 改法」写。
> 证据类型逐条标注：**真机端到端** / **运行时栈采样** / **数据落盘** / **单测守门** / **静态检查**。

---

## 一、症状与证据

### 1.1 工具卡片永久转圈、文件没写进去

| 证据类型 | 内容 |
|---|---|
| 真机端到端 | 卡片文案 `write_file 写入 /Users/macmini/Downloads…`，常驻转圈；`~/Downloads` 下无对应文件 |
| 数据落盘 | 会话 `01M3PW2S7K8MH94C6JR8GAQAE5`（title「在 Download 里面写一个 Helo」，created 23:21:09 → updated 23:21:27）**没有任何 assistant 消息** |
| 数据落盘 | 同一时段 `audit_log` 里**一条 `ai` 记录都没有**（既无 `write_file`、也无 `exec`）。最后一条 ai 审计是 02:55 的 takeover → **工具根本没被执行过** |

> 这条最关键：审计表没有 `write_file`，说明请求**从来没走到** `fs.write_file`，
> 而不是「写了但写错地方」。排查方向因此从一开始就锁定在「执行前的确认环节」。

### 1.2 停止按钮点了没反应

| 证据类型 | 内容 |
|---|---|
| 代码 | 前端 `stop()` 先本地 `setAiBusy(false)` 再 `await aiApi.cancel(id)` → 界面上的转圈会消，**但后端命令本身也堵在同一把锁上**，`cancel_job` 里的 `confirm.0.send(Some(Deny))` 永远拿不到写锁 |

### 1.3 之后再提问彻底没回复

| 证据类型 | 内容 |
|---|---|
| 运行时栈采样 | `sample 9953 3`：两个 worker 线程分别停在 `nexterm_lib::commands::ai::ai_chat` 与 `InvokeResolver::respond_async_serialized_inner`，深处都压在 `std::sys::sync::rwlock::queue::RwLock::lock_contended`（`semaphore_wait_trap`），中间帧含 `tokio::sync::watch::Sender<Option<ConfirmDecision>>::send_*` |
| 运行时栈采样 | 两个 worker 的任务栈**完全同构** → 与「多轮会话里多次 `wait_confirm` 全部堵同一把锁」吻合 |
| 真机端到端 | 卡死期间 `lsof -p 9953 -iTCP` **无输出** → 不是网络挂起，是纯内存锁死锁 |

---

## 二、根因：`wait_confirm()` 自己握着读锁又要写锁

`src-tauri/src/ai/mod.rs` 原写法（修复前）：

```rust
if let Some(d) = *rx.borrow_and_update() {
    let _ = self.confirm.0.send(None); // ← 死在这里
    return d;
}
```

三条事实叠加，缺一不可：

1. `AiJob.confirm` 是 `(watch::Sender, watch::Receiver)<Option<ConfirmDecision>>`；
   `tokio::sync::watch` 内部用的是 `tokio::loom::sync::RwLock`，**非 loom 构建下就是 `std::sync::RwLock`**。
2. `rx.borrow_and_update()` 返回的 `Ref` **握着那把锁的读锁**；
   `if let` 的临时量生命周期是**整个 `if let` 表达式**（`then` 块内仍然活着），
   **edition 2021 / 2024 都一样** —— 2024 只改了 `else` 分支的时机。
3. `self.confirm.0.send(None)` 要**同一把锁的写锁**。`std::sync::RwLock` **不可重入** → 当场自死锁，
   且读锁**永不释放**。

于是：第一个需要用户确认的工具（`write_file`）走 `guard` 的 `Decision::Ask` 分支 →
推 `AiEvent::ConfirmRequired` → `job.wait_confirm().await` → **卡死**。
`ai_confirm` / `ai_cancel` 后续的每一次 `send` 都排队等在这把读锁上 → 症状 1.2 / 1.3。

### 独立复现实验（与 edition 无关）

`/tmp/nex-iflet/m.rs`：用 `Deref` 包装 `RwLockReadGuard`，复刻同一写法 `if let Some(v) = *borrow(&l) { l.try_write() }`：

| edition | 块内 `try_write()` | 块外 `try_write()` |
|---|---|---|
| 2021 | **false** | true |
| 2024 | **false** | true |

→ 证明读锁确实活到 `then` 块内，**不是本仓库 edition 造成的巧合**。

---

## 三、改法

### 3.1 后端：拆语句（`src-tauri/src/ai/mod.rs`）

```rust
// 单独一条语句取值：临时 `Ref` 在本语句结束就释放读锁，
// 下一句 `send(None)` 才拿得到写锁。
let decision = *rx.borrow_and_update();
if let Some(d) = decision {
    let _ = self.confirm.0.send(None);
    return d;
}
```

并在函数与测试上补了大段 ⚠️ 注释，把「为什么不能合成一行」写死在代码里 —— 这是**极易被后人"顺手简化"回去**的写法。

### 3.2 回归测试：`wait_confirm_resets_slot_without_holding_read_guard`

要点：**不用 `tokio::time::timeout`**。死锁是**线程级阻塞**，同一条任务被卡住时定时器没法在同一 worker 上开火；
改用**纯 std 线程 + `recv_timeout`**，无论卡多久都能把「超时」变成一次明确的断言失败。

### 3.3 回归测试：三条，覆盖三个症状面

| 用例 | 守的是 | 修复前 | 修复后 |
|---|---|---|---|
| `wait_confirm_resets_slot_without_holding_read_guard` | 症状 1：单轮「读到决定 → 复位槽位」 | **FAILED**（5.00s 超时） | ok（0.00s） |
| `confirm_roundtrip_survives_multiple_turns` | 症状 3：连续三轮确认，每轮都要能独立返回 | **FAILED** | ok |
| `cancel_job_unblocks_wait_confirm` | 症状 2：取消能把等待中的确认解开 | **ok（修复前后都绿 —— 见下）** | ok |

两个共同要求：

1. **不用 `tokio::time::timeout`**。死锁是**线程级阻塞**（堵的是 OS 线程，不是 future），
   同一条任务被卡住时定时器没法在同一个 worker 上开火 —— 用例会**静默挂死**而不是明确失败，
   那正是「看起来跑过了」的假绿。统一改用**纯 std 线程 + `recv_timeout(5s)`**：
   无论卡多久都能把「卡死」变成一次明确的断言失败。
2. **构造 `AiJob` 不要套两层 `Arc`**：`AiJob::new()` 本身已返回 `Arc<Self>`。

> ⚠️ **一条如实标注**：`cancel_job_unblocks_wait_confirm` 在修复前后**都是绿的**。
> 原因是 `cancel_job` 先 `cancel.cancel()` 再 `send(Some(Deny))`，`wait_confirm` 的
> `tokio::select!` 会先命中 `cancelled()` 分支直接返回 Deny，**根本没走到复位槽位那句** ——
> 所以它守的是「取消能把等待解开」这条**语义**，不是本次死锁本身。
> 不把它算作 red→green 的对照，避免夸大证据强度。

### 3.4 前端：确认按钮不再静默失败（`src/features/ai/AiSidebar.tsx`）

`confirm()` 原来 `if (!jobId) return;` —— 在「确认卡片先于 `ai_chat` 回填 `jobId` 渲染出来」的极短窗口里，
**点了完全没反应、也没提示**，与本次症状同构。改为：缺 `jobId` 时给 `info` toast；
`aiApi.confirm` 抛错时给 `error` toast 并**保留卡片让用户重试**，不再静默吞掉。

---

## 四、验收证据

### 4.1 反向验证（红/绿对照）

把 `wait_confirm` **临时改回** buggy 写法（只动取值/复位那两行，测试不动），同一批用例重跑：

```
test ai::tests::cancel_job_unblocks_wait_confirm ... ok
test ai::tests::confirm_roundtrip_survives_multiple_turns ... FAILED
test ai::tests::wait_confirm_resets_slot_without_holding_read_guard ... FAILED

---- ai::tests::confirm_roundtrip_survives_multiple_turns stdout ----
thread 'tokio-rt-worker' panicked at src-tauri/src/ai/mod.rs:494:23:
wait_confirm 卡死了（见本文件 wait_confirm 上的 ⚠️ 注释）

test result: FAILED. 7 passed; 2 failed; ... finished in 10.12s
```

实验脚本带 `trap ... EXIT` 自动还原，事后核对 **sha256 与实验前一致**
（`6f0d6d56…`）→ 仓库里留下的是修复版，不是实验版。

**判据强度**：两条用例从红到绿，且红的**失败点就是被修的那一行**（不是别处连带挂掉）。
这比「改了某处 + 测试变绿」强一档 —— 它排除了「碰巧变绿」。

### 4.2 质量门与真机

| 项 | 结果 |
|---|---|
| 单测（修复前） | 见 4.1：2 failed / 10.12s，panic 文案指向 `wait_confirm` 的复位语句 |
| 单测（修复后） | 三条确认链用例全 ok；`cargo test -p nexterm --lib` → **全部通过** |
| 静态 | `cargo fmt --all --check` exit 0 |
| 静态 | `cargo clippy --all-targets --all-features -- -D warnings` exit 0 |
| 静态 | `pnpm typecheck` exit 0 / `pnpm lint`（`--max-warnings 0`）exit 0 |
| 真机（装机） | 旧二进制 `UUID 78B2A0F3` 备份到 `~/Library/Application Support/NexTerm/backup/nexterm-78B2A0F3.bin`；新构建打进 `/Applications/NexTerm.app`，**ad-hoc 重签**后 `UUID 49BC0226`、`flags=0x10002(adhoc,runtime)` |
| 真机（启动） | 新实例起来，日志 `2026-09-29T15:45:19Z INFO boot: NexTerm 内核就绪`，无 ERROR/WARN |

> **⚠️ 未做真机 AI 端到端**：本机 LLM 端点 `127.0.0.1:8888` **没有在跑**（`nc -z` 失败），
> 无法在真机上跑「让 AI 写文件 → 弹确认 → 点允许 → 文件落盘」这条完整链路。
> 这一段的证据强度只到「逻辑级 + 单测级 + 装机启动级」，**不宣称端到端已验过**。
> 端到端留待用户在自己的 LLM 环境里复验，步骤见 §5。

### 4.3 装机身份核验（sha256 在重签后会变，别拿它当唯一判据）

| 对象 | UUID | sha256 |
|---|---|---|
| 构建产物 `target/release/nexterm` | `49BC0226` | `5803dd4d…` |
| 装机后 `/Applications/NexTerm.app/…/nexterm` | `49BC0226` | `ee31e7e1…` |
| 旧（备份） | `78B2A0F3` | — |

**装机后 sha256 变了是预期的**：`codesign` 会重写链接器预留的 ad-hoc 签名块
（体积 25,094,160 → 24,966,400 字节）。**可靠的身份判据是 `LC_UUID`** ——
重新签名不改它。若只比 sha256，很容易误判成「装错版本」。

## 五、复验步骤（用户在自己的 LLM 环境里跑）

1. 启动本地模型端点（默认 `http://127.0.0.1:8888`），或在设置里切到可用模型。
2. 开一个**本地终端**标签（用户本次就是在本地终端上触发的，不是 SSH）。
3. 在 AI 侧栏发：「在 ~/Downloads 里写一个 HelloWorld.txt」。
4. 预期：出现确认卡片 → 点「允许」→ **卡片收起、不再转圈** → 文件真的落盘。
5. 再点一次「停止」（本轮运行中）→ 预期 **立刻停止并出现「已停止本轮」提示**。
6. 停止后**继续追问** → 预期 正常有回复（修复前这一步会彻底卡住）。

第 4–6 步全过，才算这次修复在真机上闭环。

## 六、遗留 / 未做

- **未做**：给 `wait_confirm` 加超时或心跳兜底。任何单点死锁都不该再次表现为「永久转圈」，
  但自动超时会让「用户长时间思考」被误判成拒绝，语义上要谨慎设计，不在本轮动。
- **未做**：`sqlx` worker 采样时停在 `semaphore_wait_trap`（疑似等 BUSY 超时）。
  无法断定与本次死锁是同一时序，**如实标为未证实**，不硬凑结论。
- **未做**：`exec` 的 8s 超时对慢命令可能过紧，待单独评估。
- **未做**：前端 `confirmCard`/`items` 里 `jobId ?? ""` 的**根因清理** ——
  卡片构造发生在 `ai_chat` 回填 `jobId` 之前，因此存进 `items` 的 `jobId` 是空串。
  目前它不影响行为（`items` 里的 confirm 卡片渲染为 `null`，真正点的是 `confirmCard`，
  而 `confirm()` 读的是 state 而非卡片字段），本轮只做了「不静默失败」的兜底。

