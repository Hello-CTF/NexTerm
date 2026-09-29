# 改写远端已有文件必然失败：备份写错了句柄（2026-09-30）

> 触发：真机截图。AI 对 `/home/linuxcore/Helloworld` 调 `write_file`，返回
>
> ```text
> 写入失败: SFTP 错误: No such file
> ```
>
> 用户的原话：「总会遇到这种情况 导致使用命令去输入又要多一轮 看看是什么情况」。
>
> 注意这个报错有多反常：**文件明明在** —— 同一轮里 `read_file` 刚成功读过它
> （「已重新读取当前文件内容（1573 字节，v2 版）」）。「No such file」指向的方向是错的。

## 一、结论

| 问题 | 结论 |
|---|---|
| 什么情况下失败 | **只**在「目标文件已存在 + 走 SFTP」这一种组合下失败。新建成功，本地文件成功 |
| 为什么 | `SftpFs::write_file` 的**备份分支把内容写进了源文件那个只读句柄**，而真正该被写的备份文件从头到尾没人碰（§2） |
| 为什么报 `No such file` | 对只读 fd 写 → `EBADF`；OpenSSH 的 `errno_to_portable()` 把 `EBADF` 归进 `SSH2_FX_NO_SUCH_FILE`，于是「拿只读句柄去写」伪装成了一个「文件不存在」（§2.2） |
| 用户的代价 | 每改一次远端文件，模型先把整份内容生成完 → 失败 → 退回 `cat > file << 'NEXEOF'` **再生成一遍**。用户说的「多一轮」就是这个，而且烧的是 token 不是时间 |

## 二、真因

### 2.1 备份分支写错了对象

```rust
let src = self.sftp.open(&path).await?;          // 只读打开**原文件**，句柄 A
let dst = self.sftp.create(&backup_path).await?; // 创建**备份文件**，句柄 B
let (mut r, mut w) = tokio::io::split(src);      // ← 拆的是 A
r.read_to_end(&mut all).await?;                  //   从 A 读
tokio::io::copy(&mut all.as_slice(), &mut w).await?; // ← 写进 A 的写半边（只读！）
w.flush().await?;
dst.sync_all().await.ok();                       //   B 从头到尾没被写过
```

两处坏：

1. **写错句柄**：内容写回源文件那侧的只读句柄，不是 `dst`。这一步必然报错 ⇒ 整个
   `write_file` 失败 ⇒ 用户的文件压根没被改。
2. **`dst` 被创建却从未写入**：即便某些服务器容忍了那次写，`{path}.nexterm-bak`
   也是个**空文件** —— 比没有备份更坏，用户以为有退路。

正确的写法根本不需要 `split`：`File` 同时实现 `AsyncRead` + `AsyncWrite`，
拿两个**各自独立**的句柄直接 `tokio::io::copy(&mut src, &mut dst)` 就行。

### 2.2 为什么错误信息是「No such file」

OpenSSH `sftp-server` 的 `errno_to_portable()` 把 errno 归并成 SFTP 状态码，其中：

```c
case ENOENT:
case ENOTDIR:
case EBADF:      /* ← 就是它 */
case ELOOP:
    ret = SSH2_FX_NO_SUCH_FILE;   /* 人读作 "No such file" */
```

对只读 fd 调 `write()` 得到的是 `EBADF`（Bad file descriptor），于是被翻译成一个
**看起来毫不相关**的「文件不存在」。这也是为什么这条 bug 容易查偏 —— 报错在指证
「路径写错了」，而路径一直是对的。

### 2.3 范围正好落在 `backup && exists()`

`write_file`（`tools/server.rs`）与 `edit_file`（`tools/edit.rs`）**都硬编码
`backup=true`**。所以只要目标存在就必然进这段分支：

| 目标 | 备份分支 | 结果 |
|---|---|---|
| 远端 + **新建** | 不进（`exists()` 为假） | ✅ 成功 |
| 远端 + **已存在** | **进** | ❌ 必失败 |
| 本地 + 任意 | 走 `transport/local.rs`（用的是 `tokio::fs::copy`，写对了） | ✅ 成功 |

## 三、证据一：应用自己的审计日志

`audit_log` 里这条会话的完整轨迹（时间已换算为本地时间）：

```text
01:13:08  connect
01:13:53  read_file    exit=1     ← 文件还不存在
01:14:06  write_file   exit=0     ← 新建成功（不进备份分支）
01:14:43  read_file    exit=0     ← 文件已存在
01:15:06  write_file   exit=1     ← 改写，失败
01:15:54  write_file   exit=1     ← 改写，失败
01:16:21  exec_commands exit=0    ← 退回 cat > file << 'NEXEOF'，成功
```

**4 次成功的 `write_file` 全部是「新建」，3 次失败全部是「改写」。** 规律干净到
不需要额外解释。

> 小提醒：审计表里每次 AI 工具调用都会有一条粗粒度的 `kind='exec'` 行
> （工具名在 `payload_json.tool` 里），成功时 `write_file` **额外**再写一条
> `kind='write_file'`。所以「`kind='write_file'` 只有 4 条」= 只有 4 次成功。

## 四、证据二：用**真** OpenSSH sftp-server 复现（红 → 绿）

新增 `src-tauri/tests/sftp_write_backup.rs`。它不连网络、不需要 sshd：
`socketpair` 的一头当子进程的 stdin + stdout，另一头给 `SftpSession`。

**为什么用真服务器而不是自己写 mock handler**：mock 会照我的理解编一个错误码，
而这里要验的恰恰是**真服务器的错误码** —— 上面 §2.2 那个 `EBADF → No such file`
的映射只有真实现才给得出来。

先建红（**修复前的代码**，测试文件 sha256
`a457c93149f46f59e2439d9eb43f27cff880951b2b627961bf151f83e8166d16`）：

```text
writing_an_existing_file_keeps_a_faithful_backup ... FAILED

改写**已存在**的文件必须成功 —— 真机这里报的是
「SFTP 错误: No such file」（对只读句柄写 → EBADF → OpenSSH 把它映射成
NO_SUCH_FILE）: Sftp("No such file")
```

**`Sftp("No such file")` 与真机截图里的 `写入失败: SFTP 错误: No such file`
一字不差。** 修完同一个测试文件（未再改动）转绿：

```text
test writing_an_existing_file_keeps_a_faithful_backup ... ok
test result: ok. 1 passed; 0 failed
```

测试守三件事：

1. 改写已有文件**必须成功**（真机失败的那条）；
2. 备份内容必须**等于原内容** —— 空的备份比没有备份更坏，所以专门断言这一条；
3. 新建文件不许被备份分支拦住，且**不许凭空造一个空备份**（防止修 1 的时候改坏 2）。

## 五、改动

| 文件 | 改动 |
|---|---|
| `transport/ssh.rs` | 备份分支改为两个独立句柄 `tokio::io::copy(&mut src, &mut dst)`；去掉 `split`；**成功写备份之前不继续往下写原文件**（否则等于丢了唯一的退路还在原文上盖新内容）；四处裸 `AppError::Sftp(e.to_string())` 改成带上下文的消息（`备份失败（打开原文件 …）` 等）—— 原来的裸消息正是这条 bug 难定位的原因之一 |
| `tests/sftp_write_backup.rs`（新） | 真 OpenSSH sftp-server 的复现测试；找不到 `sftp-server` 时跳过（Windows / 精简容器） |

`transport/local.rs` 与 `transport/winrm.rs` 各自的备份路径（`tokio::fs::copy` /
`Copy-Item`）**本来就是对的**，这次故障**只在 SFTP 一条路上**。

## 六、验收

| 项 | 结果 |
|---|---|
| `cargo test --workspace` | 201 + 2 + **1（新）** + 1 passed，0 failed |
| `cargo fmt --all --check` | exit 0 |
| `cargo clippy --all-targets --all-features -- -D warnings` | exit 0 |
| 装机 | UUID `51C198ED-62FA-358C-8D8E-13505B414D09`，旧版备份为 `backup/nexterm-B365FAF6-….bin` |
| 运行实例 | pid 93658，01:37:03 启动，日志 **0 ERROR** |

### 一处操作失误，必须记下来

**覆盖一个正在运行的已签名二进制，macOS 会把那个进程杀掉。** 本次 `cp` 覆盖之后
旧实例（pid 91977）直接消失，日志里没有正常的「窗口收到关闭请求」，只有上次的
启动记录 —— 即被 SIGKILL，不是用户退出的。

- 正确顺序：**先退出应用 → 再覆盖二进制 → 再启动**。
- 这也解释了为什么上一轮同样 `cp` 却没出事：进程是否已经被换掉的页命中是**碰运气**的，
  不能当成「上次没事所以这次也没事」。
- 代价：用户手上那个绑定了 `sessionId`/`tabId` 的 AI 会话作用域随之失效。
  已重新拉起应用（AI 会话本身落库，还在）。

## 七、遗留

- **服务器上可能残留 0 字节的 `.nexterm-bak`**：旧代码在拷贝之前就 `create` 了备份
  文件，失败后它留在原地，反复失败反复 TRUNC，所以它一直是空的。
  这类文件比没有备份更坏（假的退路）。**没有自动删除** —— 那是用户的文件，
  只给出排查命令：
  ```bash
  find ~ -name '*.nexterm-bak' -size 0 -print        # 空备份
  ls -l /home/linuxcore/*.nexterm-bak               # 本次涉及的那个
  ```
- **备份是全量拷贝**：改一个 100 MB 的文件会先把整份读出来再写一份。现在的实现是
  流式 `copy`（不再先 `read_to_end` 到内存），但**网络往返量仍然是两份文件**。
  真要省得改成「只在超过阈值时才备份」或远端 `cp -a`。
- `edit_file` 与 `write_file` 共用同一条备份路径，本次一并修好，但没有单独的真机验证。
- 审计表的 `kind` 是粗粒度分类（所有 AI 工具调用的通用行都记成 `exec`），
  查历史时容易误判；工具名在 `payload_json.tool` 里。
