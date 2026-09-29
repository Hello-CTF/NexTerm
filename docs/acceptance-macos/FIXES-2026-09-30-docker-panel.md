# Docker 面板：容器 / 镜像管理的错乱、延迟与批量操作

- 日期：2026-09-30
- 起因：用户报告三件事 ——
  1. 容器页 / 镜像页**会发生错乱**（截图：容器 tab 下混进了镜像行）；
  2. 删除**有很大的延迟感**；
  3. **没办法批量勾选操作**；
  4. 单个删除**多次点击后会错乱、残影**。
- 结论：四条其实是**同一条链上的四个症状**。根因两根：React key 重复 + 删除传错引用（且失败静默）。
  修复后旧现象的**确定性复现**在新组件上不再出现（§5 对照表）。

## 一、改动前的真实状态

| 位置 | 改动前 | 后果 |
|---|---|---|
| 镜像行 key | `<tr key={i.id}>` | `docker images` 里**同一个 image ID 会挂多个 repository**（见 §2 本机实测）→ key 重复。React 官方口径：*"may cause children to be duplicated and/or omitted"*，行为未定义 |
| 两个 `<table>` | 同类型同位置、**都没有 key** | 切 tab 时 React 复用同一个 DOM 节点，只有子节点按 key diff；配合上面的重复 key，旧的镜像行会**残留在容器表里** |
| 删镜像的引用 | `imageRemove(sessionId, i.id)` | 同 ID 多 repository 时 `docker rmi <image-id>` 被 daemon **直接拒绝**（§2），而这是**最常见的多 tag 场景** |
| 删除的错误处理 | 没有 `catch` | 失败**完全不发声**：没有 toast、行也不消失 → 用户以为没生效 → **反复点** → 并发请求互相打架 = 「多次点击后错乱」 |
| 删除的反馈 | 只有 `await` + `invalidateQueries` | 一行删除要等 `docker rmi` 往返 + 重新拉列表；而**永远失败**所以永远等不到 → 「很大的延迟感」 |
| 并发保护 | 无 | 删除按钮不 disabled，同一行可以连点 N 次 |
| 批量操作 | 无 | 没有 checkbox、没有全选、没有批量删除 |

## 二、本机实测（真实 docker，不是推断）

```console
$ docker images --format '{{json .}}'
{"ID":"7dcddc01f13b","Repository":"mysql","Tag":"8.0","Size":"1.09GB","CreatedSince":"4 months ago"}
{"ID":"7dcddc01f13b","Repository":"docker.m.daocloud.io/library/mysql","Tag":"8.0",...}
#   ↑ 同一个 ID 出现两次（同一镜像的两个 repository 名）

$ docker image inspect 7dcddc01f13b --format '{{json .RepoTags}}'
["mysql:8.0","docker.m.daocloud.io/library/mysql:8.0"]

$ docker rmi 7dcddc01f13b
Error: conflict: unable to delete 7dcddc01f13b (must be forced)
        - image is referenced in multiple repositories
#   ↑ 前端传的正是这个 id ⇒ 删除 100% 失败，且（改动前）静默
```

## 三、做了什么

- **行 key 唯一化**：新增 `imageKey(i) = ${i.id}|${i.repository}:${i.tag}`（容器仍用 `id`）。
- **两张表各自 `key="containers"` / `key="images"`**：切 tab 强制**重建 DOM**，不再复用。
  注释写清了原因，避免以后被"顺手去掉"。
- **删除引用改 `repository:tag`**：`imageRef(i)` —— 同 ID 多 tag 时唯一不会失败的形式；
  悬空镜像（`<none>`）回退到 `id`。
- **补 `catch` + toast**：失败**必须吵出来**（`describeError` 带原文），这正是上一版让人反复点的原因。
- **乐观更新 + 回滚**：删除先立即从列表拿掉，失败再把快照放回去。消除"延迟感"的机制性来源。
- **行级 `pending`**：删除中的行置灰 + 按钮 `disabled`，挡住"点了没反应就再点一下"的并发删除。
- **批量勾选**：容器 / 镜像列表各加 checkbox 列 + 表头全选（带 `indeterminate` 半选态）+
  选中计数 + 「删除选中 (N)」+ 「取消选择」。切 tab 会清空选择 —— 两个列表的行 key 语义不同，
  留着旧选择会误删别的东西。批量删除**串行**执行（daemon 对 `rmi`/`rm` 本就要排队，
  并发只会互相打架），结束后报「成功 N 个」/「M 个未能删除 · <第一条原因>」。
- **演示数据补上真实形状**：`src/demo/data.ts` 的 images 加入「同一 ID 两个 repository」，
  让演示模式也能守住这个 case。

## 四、证据

### 4.1 质量门

```
pnpm typecheck                                → 通过
pnpm lint（--max-warnings 0）                  → 通过
```

> 本轮只改前端（`DockerPanel.tsx` + 演示数据），未动 Rust；
> 内核侧的 fmt / clippy / test 见同目录 `FIXES-2026-09-30-builtin-local-asset.md`。

### 4.2 真浏览器对照实验（确定性复现）

方法：把 **git HEAD 的旧组件逐字节复制**成 `LegacyDockerPanel`，与当前组件**并排挂载**
在同一页面，用同一份演示数据驱动同样的「容器 → 镜像 → 容器 → 镜像」切换，
记录每步可见表格状态、React 警告、删除时传给后端的实参。
用 headless Chrome（154.0.8037.58）经 **CDP** 驱动（`--remote-debugging-port` + Node 22 内置 WebSocket），
两个组件**分开跑**以保证警告归因干净。

| 指标 | 旧组件 | 新组件 |
|---|---|---|
| 切回容器后的行数（应为 6） | **7** | **6** ✓ |
| 串入的镜像行 | `mysql:8.0 \| 1.09GB \| 4 months ago` | **无** ✓ |
| 再切镜像后的行数（应为 9） | **10**（重复 1 行） | **9** ✓ |
| 删除传给后端的实参 | `sha256:7dcddc01f13b`（真机必失败） | `mysql:8.0` ✓ |
| React「two children with the same key」 | **3 条** | **0 条** ✓ |

旧组件里「表头是容器的、第一行却是镜像行」那一条，与用户截图**完全同形**。

### 4.3 探针自证

旧组件在这套探针下**确实红了**（3 条 key 警告 + 1 行串入 + 1 行重复 + 传错实参），
说明探针不是空跑 —— 新组件的「全绿」才有意义。

## 五、未验证边界（如实标注）

- 批量删除的**真机端到端**（连真 docker daemon 删多个容器 / 镜像）未实测；
  验证用的是演示模式的 mock。mock 的 `docker_image_remove` 同时接受 `id` 与 `repo:tag`，
  所以 mock 本身**测不出**"传 id 会失败"——这一点是本机真 docker（§2）证明的。
- 装机后的 GUI 截图证据见本轮装机记录（另附）。
