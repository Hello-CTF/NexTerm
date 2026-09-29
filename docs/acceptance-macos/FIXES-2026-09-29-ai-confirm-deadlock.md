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

### 3.3 前端：确认按钮不再静默失败（`src/features/ai/AiSidebar.tsx`）

`confirm()` 原来 `if (!jobId) return;` —— 在「确认卡片先于 `ai_chat` 回填 `jobId` 渲染出来」的极短窗口里，
**点了完全没反应、也没提示**，与本次症状同构。改为：缺 `jobId` 时给 `info` toast；
`aiApi.confirm` 抛错时给 `error` toast 并**保留卡片让用户重试**，不再静默吞掉。

---

## 四、验收证据

| 项 | 结果 |
|---|---|
| 单测（修复前） | `wait_confirm_resets_slot_without_holding_read_guard` **FAILED**，耗时正好 `5.00s`（= 超时值），panic 文案：「wait_confirm 卡死了：确认槽位复位时自己握着读锁又要写锁」 |
| 单测（修复后） | 同一用例 **ok**，`finished in 0.00s` |
| 全量单测 | `cargo test -p nexterm --lib` → **180 passed; 0 failed**（47.35s） |
| 静态 | `cargo fmt --all --check` → exit 0 |
| 静态 | `cargo clippy --all-targets --all-features -- -D warnings` → exit 0 |
| 静态 | `pnpm typecheck` → exit 0 |
| 静态 | `pnpm lint`（`--max-warnings 0`）→ exit 0 |

> 「修复前红、修复后绿」这个对照是本轮最关键的一条：
> 它把「我改了某处」升级成「我改的正是让这条用例红的那个原因」。

---

## 五、遗留 / 未做

- **未做**：给 `wait_confirm` 加超时或心跳兜底。任何单点死锁都不该再次表现为「永久转圈」，
  但自动超时会让「用户长时间思考」被误判成拒绝，语义上要谨慎设计，不在本轮动。
- **未做**：`sqlx` worker 采样时停在 `semaphore_wait_trap`（疑似等 BUSY 超时）。
  无法断定与本次死锁是同一时序，**如实标为未证实**，不硬凑结论。
- **未做**：`exec` 的 8s 超时对慢命令可能过紧，待单独评估。
