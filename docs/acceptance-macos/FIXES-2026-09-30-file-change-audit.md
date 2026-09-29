# 文件变更可审：让「每次写文件生成修改前后 diff」真的成立

- 日期：2026-09-30（承接 2026-09-29 的确认链死锁修复）
- 起因：探宝指出 README 里承诺的「**文件变更可审** —— 每次写文件生成修改前后 diff，改动一目了然」
  （`README.md:49`，官网同款文案在 `website/index.html:95`）在实际行为里明显缺失。
- 结论：**功能本身在，但承诺有三处不成立**。已修两处，第三处如实列为遗留。

## 一、承诺 vs 实测

| # | 承诺 | 实测（改动前） | 判定 |
|---|---|---|---|
| A | **每次**写文件生成修改前后 diff | 只有「文件已存在且能读成文本」才推 `FileChange`。**新建文件（`before` 读不到 = `None`）整条事件都不推** —— 而新建恰恰是 AI 写文件最常见的形态 | ❌ **已修** |
| B | 改动**一目了然** | 「一目了然」发生在**执行之后**：确认卡片的 `rendered` 是 `工具名 + 原始 JSON 参数`，用户在点「允许」前看不到要改什么 | ❌ **已修**（改成批准前给逐行 diff） |
| C | **可审**（事后能回看） | `fileChange` 事件**不落库**：`agent.rs` 只 `msg_insert` user / assistant 两条文本。切走会话再回来，变更记录消失，只剩审计表里的原始 args | ⚠️ **未做**（见 §6） |
| D | `edit_file` 也要有对照 | 走同一条路径，但以前同样受 A 影响（改动前 `apply_edit` 的判定压根没用上，diff 靠"执行后再读一遍"倒推） | ✅ 已修（现在用 `apply_edit` 预判，与工具执行同源） |

`docs/images/ai-file-changes.png` 那张 README 配图是真的（截图来自演示模式），
所以这不是"功能没实现"，而是**实现漏了新建文件与批准前这两个位置**。

## 二、根因

`src-tauri/src/ai/agent.rs`，改动前的 `execute_with_diff`：

```rust
let before = match &target {
    Some(p) => read_text(state, scope, p).await,   // 文件不存在 → None
    None => None,
};
let outcome = tools::execute(...).await;
if let (Some(p), Some(before)) = (target, before) {   // ← before 是 None 就直接跳过
    if let Some(after) = read_text(state, scope, &p).await { ... }
}
```

`read_text` 对「文件不存在」「是二进制」「超过 1MB」「无权限」**返回同一个 `None`**，
于是四种情况被压成一个分支：整张变更记录卡片不推。四种情况的正确处置其实不同：

| 情况 | 该怎么算 | 旧行为 |
|---|---|---|
| 文件不存在（新建） | `before = ""`，diff 全是新增 | ❌ 整条不推 |
| 读到文本 | 正常对照 | ✅ |
| 二进制 / 超限 / 无权限 | 既不能报"原样保留"也不能报"全部新增"，两种都是编的 → 不推 | ✅（碰巧对了） |

再加一条：读文件读不出**是否"不存在"**，不看错误类型看不出来 ——
`LocalFs::read_file` 对不存在的文件返回 `AppError::Io(NotFound)`（不是 `AppError::NotFound`），
而 SFTP / WinRM 的返回未必一致。靠错误类型反推等于把各 Transport 的实现细节焊进来。

因此改动引入显式三态 `BeforeText::{Missing, Text, Unreadable}`，
并且**先问 `FileSystem::exists()` 再读**，不看错误类型。

## 三、改了什么

| 文件 | 改动 |
|---|---|
| `src-tauri/src/ai/agent.rs` | 新增 `BeforeText` 三态、`ChangePreview`、`preview_change()`、`planned_after()`、`change_record()`；`read_before()` 先 `exists()` 后 `read_file()`；`execute_with_diff` 改为「执行前取数 → 执行 → 执行后重读校准」。删掉旧的 `read_text` |
| `src-tauri/src/ai/mod.rs` | `AiEvent::ConfirmRequired` 增 `reason` 与 `preview: Option<FilePreview>`；新增 `FilePreview { path, before, after, kind }`；camelCase 回归测试扩展到嵌套结构与 `args` 必须保持 snake_case |
| `src-tauri/src/ai/takeover.rs` | `send_keys` 的确认事件补 `reason` / `preview: None`（往终端打字不是写文件） |
| `src/features/ai/diff.ts`（新） | `simpleDiff` / `toLines` / `diffLineText` / `hasVisibleChange` 抽成**无依赖纯模块**，确认卡片与变更记录共用同一份算法 |
| `src/features/ai/AiSidebar.tsx` | 新增 `ConfirmBody`（批准前渲染 diff + 判定理由 + 折叠的原始参数）与 `DiffLines`；`confirmRequired` 读 `reason` / `preview` |
| `src/demo/mock.ts` | 新增「写文件」演示场景（**新建与修改各一条**），预览与变更记录共用同一对常量 |
| `src/styles.css` | Diff 配色的注释更新（现在两处卡片共用） |
| `README.md` / `website/index.html` | 描述改成与实现一致：明确是"确认卡片上先给 diff"，不再用无条件的"每次" |

设计取舍（写在代码注释里，都在 `agent.rs`）：

- **预览与变更记录共用一份取数**：两处各读一次文件的话，同一个改动会在「允许之前」和
  「执行之后」显示成两个样子，用户会以为中间又变了一次。
- **`edit_file` 的 after 用 `tools::edit::apply_edit` 真算**，不是猜：那是工具执行时用的同一个纯函数。
  算不出来（找不到 `old_string` / 命中多处没开 `replace_all`）就**不给预览**，工具马上会报同样的错。
- **执行后的 after 以重读为准**（真实落盘结果），重读不出来时才退回预测值。
- `frontend_preview` 在有 `before == after` 或两侧合计超 512KB 时返回 `None`：
  原始参数里本来就带着全文，所以是"少了个视图"，不是"少了信息"。

## 四、反向验证（红 / 绿对照）

把新行为临时改回旧写法（`Missing => return None` / `=> None`）重跑，脚本带 `trap` 自动还原：

```
new_file_still_gets_a_change_record ............... FAILED   ← 新建文件必须生成变更记录
preview_tells_create_from_modify .................. FAILED   ← 新建文件要给预览
preview_skips_pairs_too_big_to_review ............. FAILED
（其余 9 条 ok）

test result: FAILED. 9 passed; 3 failed
FIXED_SHA     = 01a21b9e216a0a29c1d90c234f8a0a233b62e47894bb9af6dab65149faeccc95
RESTORED_SHA  = 01a21b9e216a0a29c1d90c234f8a0a233b62e47894bb9af6dab65149faeccc95
```

红的三条，失败点就是被改掉的那两行。还原后 sha256 与实验前一致（脚本没污染工作区）。

保留代码后的全量结果：

```
cargo fmt --all -- --check                                  exit 0
cargo clippy --all-targets --all-features -- -D warnings    exit 0
cargo test -p nexterm --lib                                 193 passed; 0 failed（改动前 182）
cargo test --workspace                                      全绿（含 local_pty / ssh_e2e）
pnpm typecheck                                              exit 0
pnpm lint (--max-warnings 0)                                exit 0
```

## 五、前端渲染的真跑证据

前端没有测试框架（见 §6），所以把 diff 算法抽成无依赖模块 `src/features/ai/diff.ts`，
**直接跑真模块**取证（不是复制一份算法）：

```
node --experimental-strip-types probe-diff.ts     # 探针在 /tmp，未入库
```

输出（节选）：

```
── 新建单行文件（before 是空串）       → "+ Hello"
── 新建多行文件                        → "+ server {" / "+   listen 80;" / "+ }"
── 修改已存在文件                      → 3 条 "-" + 4 条 "+"
── 内容完全相同                        → 渲染提示：（内容没有变化）
── 只差文件末尾的换行                  → 渲染提示：（差异在文件末尾的换行）
── 单行替换                            → "- listen 80;" / "+ listen 8080;" / "  root /var/www;"

✅ 新建文件只渲染一条 `+ Hello`（空 before 不产生幽灵 `- `）
✅ 新建多行文件是 3 条增行（末尾换行不产生多余 `+ `）
✅ 修改文件同时给出删行与增行
✅ 只差末尾换行时不谎称有增删
✅ 完全相同同样不谎称有增删
✅ 真有改动时要认得出
```

**探针发现并已修掉一个真问题**：`"a"` → `"a\n"`（只差末尾换行）时逐行 diff 比不出增删，
卡片会渲染成"一段没变的内容"，看起来像卡片坏了。现在 `hasVisibleChange` 判定后改说人话。

### 真 UI 证据（演示模式）

`pnpm dev` + 浏览器（URL 带 `?demo=1` 强制演示模式；演示模式的所有数据都是内存假数据）：

| 截图 | 内容 |
|---|---|
| `26-ai-confirm-diff-modify.png` | 修改文件：确认卡片直接给出「将修改 /etc/nginx/nginx.conf」+ 逐行 diff（3 删 4 增）+ 判定理由 + 折叠的「查看原始参数」 |
| `27-ai-confirm-diff-create.png` | **新建文件**：确认卡片给「将新建 /etc/nginx/conf.d/upload.conf」+ 2 条 `+`，**没有幽灵 `- ` 空行** |
| `28-ai-changerecord-create.png` | 允许之后：变更记录卡片显示同样的两行 `+`，与批准前那份预览逐字一致 |

> ⚠️ **证据强度的边界**：这三张图走的是**演示模式**（前端 + mock 事件），证明的是
> 「前端收到该形状的事件时会怎么渲染」以及「批准前的预览与批准后的记录一致」。
> 它**不是**真机 AI 端到端的证据 —— 本机 `127.0.0.1:8888` 上的本地模型端点没在运行，
> 起不了真实工具调用链路（与 2026-09-29 那次同一个限制）。
> Rust 侧的字段名（`preview` / `reason` 等 camelCase）由
> `ai::tests::every_event_field_is_camel_case` 守着；但**mock 与 Rust 事件的字段形状
> 目前没有自动校验**，靠人工对齐（见 §6）。

## 六、遗留 / 未做

1. **变更记录不落库（缺口 C）**：`fileChange` 只活在当前会话的内存里，切走再回来就没了。
   「可审」的完整含义应该包含事后回看。做之前要先定一件事：`before`/`after` 最多各 1MB，
   每条写文件都存一份会让 `data.db` 长很快（仓库里已经因为截图 base64 有过一次同样的顾虑）。
   建议的落法：单独一张表、两侧各截断到 N KB、只留最近 M 条，而不是塞进 `messages`。
2. **只差末尾换行的精确表达**：现在给一句提示，不做 `\ No newline at end of file` 标记 ——
   那属完整 diff 算法的范围，本模块刻意不做 LCS。
3. **前端没有测试框架**：`src/` 下一个测试文件都没有，所以这次的断言只能靠
   `node --experimental-strip-types` 探针（一次性的）。要不要引 vitest 是个独立决定，
   引入后 CI 也要跟着加一步 —— 不该顺手塞进这次改动里。
4. **mock ↔ Rust 事件形状没有自动校验**：演示模式是"没有本地模型时唯一能看到这个功能的地方"，
   值得加一条测试把两边对上；现在靠注释和人工核对。
5. **`exec_commands` 改文件不产生 diff**：`sed -i` / `mv` / `rm` 都不走 `write_file`。
   README 只承诺了"写文件"，不算违约；但"AI 到底动了哪些文件"这个问题在 `exec` 面前仍是空的。
6. **大文件 / 二进制**：确认卡片不给预览（>512KB 合计或读不出文本），变更记录沿用 1MB 上限。
   这类目标上，"每次写文件都有 diff"仍不成立 —— 是刻意取舍（编一个 diff 更糟），
   但 README 的措辞已经不再用无条件的"每次"。

## 七、复现命令

```bash
cd /Users/macmini/nexterm
cargo test -p nexterm --lib -- ai::agent::tests --test-threads=1
pnpm typecheck && pnpm lint

# 演示模式下的 UI 复现（新建 / 修改各一条消息）
pnpm dev                      # → http://localhost:1420/?demo=1
# 「把 nginx 配置里的 client_max_body_size 改一下，写成 64m」 → 修改
# 「帮我在 /etc/nginx/conf.d 里新建一个 upload.conf」        → 新建
```
