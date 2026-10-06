# 小分辨率 / 平板下工具条坍缩为竖排单字（CJK + flex 失效模式）

- 日期：2026-10-07
- 分支：`rust`
- 起因：用户截图报障 ——「在一些较小的分辨率或者平板设备上会出现这样的异常」。
  截图来自懒猫 dev（`https://nexterm.lazycore.heiyu.space`，1080 宽窗口、凭据视图标签页、
  AI 侧栏展开）：`凭据视图` 被压成**一个字一行的竖排**、右侧统计提示堆成 8 行、
  最右的「复制」按钮被裁掉一半。
- 结论：不是凭据视图独有的问题，而是**一类**共用的失效模式。除凭据视图外，
  同一次排查还捞出**终端工具条**与**设置 · 快捷键卡片**两处同类坍缩（§4.3、§4.4）。
  三处已一并修复，7 个文件、+123 / −27 行。

## 一、被测对象（先证明"测的是新东西"）

| 项 | 值 |
|---|---|
| 度量工具 | headless Chrome 154.0.8037.98 + CDP（`Emulation.setDeviceMetricsOverride` 设定窄视口） |
| 被测页面 | dev server `http://localhost:1420/?demo=1`（同一实例、同一探针，只切换工作区代码） |
| 修复前 | `git stash` 掉本次 7 个文件后重新采集（补丁 sha256 `6e53043e…`，pop 后复核一致） |
| 修复后 | 工作区含本次改动 |
| 产物核验 | `vite build` → `dist/assets/index-sx_ms3aL.css` / `index-DssEduua.js` |

前/后两轮**同一台机器、同一个 Chrome、同一份探针脚本**，只切换被审查的源代码 ——
这排除了"换了环境所以看着好了"这类假阳性。

## 二、根因：CJK + flex 的 min-content 坍缩

三条事实叠加，缺一不可：

1. **flex 项默认 `min-width: auto`** —— 它可以被压缩到自己的 *min-content* 宽度。
2. **中文串的 min-content 宽度 = 一个汉字**。英文串的 min-content 是**最长单词**
   （所以 `Credentials` 只会整体挪走或出省略号），中文没有单词边界，
   于是被压缩时**不会出省略号，而是逐字换行**。
3. **按钮 `.nx-btn` 是 `flex: 0 0 auto` 不参与压缩**，而图标 + 角标同样有固定宽度。
   于是压缩量**全部**落在文字上：文字塌成一列，按钮反而溢出被裁。

实测（修复前，凭据视图，面板 422px）：

```
.nx-toolbar-title  「凭据视图」     13 × 81 px   （4 行）
.nx-hint           「7 条凭据 · …」  23 × 110 px  （8 行）
button              「复制」          右溢 +53px  → 被 overflow:hidden 裁掉
bar.scrollWidth 475 > clientWidth 422
```

**为什么"较小分辨率或平板"才出现，而大屏没事**：主区宽度 ≠ 视口宽度，而是

```
主区 = 视口 − 左导航 rail − 左栏 − AI 侧栏 − 分屏
```

所以 1080 宽的窗口、只要开着 AI 侧栏，主区也只剩 **422px**（用户截图正是这个组合）；
1024 宽只剩 366px。**视口断点（`sm:`/`lg:`）在这里必然判错** —— 这也是原代码
里 `hidden sm:inline` 那个提示没能兜住的原因：`sm` 在 1080 视口下是"够宽"的，
可主区只有 422px。

## 三、修法（三层，缺一不可）

| 层 | 做什么 | 落点 |
|---|---|---|
| ① 文字不参与压缩 | `.nx-toolbar-title` 加 `flex:0 0 auto` + `white-space:nowrap`；工具条内 `.nx-hint` 改「可压缩 + 省略号」；`.nx-select` 固定不缩 | `src/styles.css` |
| ② 按**容器**宽度依次丢弃次要项 | `.nx-toolbar` 加 `container-type: inline-size`，各工具条用 `@min-[Npx]:` 决定收掉哪些文字 | `styles.css` + 各面板 |
| ③ 兜底横滚 | `overflow-x: auto` + 隐藏滚动条（`.nx-hide-scrollbar`） | 同上 |

### 3.1 为什么必须用容器查询而不是视口断点

`container-type: inline-size` 让工具条**自己**成为一个查询容器，`@min-[Npx]:`
判的是「这条工具条有多宽」= 主区宽度，与视口无关。上面 §二 的算式说明
视口宽度在这里是个坏代理变量。

丢弃优先级（按凭据视图实测的自然宽度定）：

```
全量 665px  →  去统计提示 516  →  再去按钮文字 408  →  再去「只读」角标 367
```

按这个顺序挂阈值：提示 `@min-[680px]` / 按钮文字 `@min-[540px]` /
角标 `@min-[420px]` / 低于 367 就走横滚。终端工具条同理：
`搜索`、`录制` `@min-[430px]`，`命令块` `@min-[560px]`。

### 3.2 ⚠️ Tailwind v4 会**静默丢弃** `@max-[Npx]:`

原打算写成「默认宽 + `@max-[430px]:` 收窄」，构建后发现产物里
**一条 `max-width` 容器查询都没有** —— 写了等于没写，且**不报错、不警告**。
改为「默认窄 + `@min-[Npx]:` 放宽」后正常。产物核验：

```
1 @container (min-width:400px)     ← 设置页快捷键网格
1 @container (min-width:420px)     ← 凭据视图「只读」角标 / 凭据面板「凭据视图」按钮
1 @container (min-width:430px)     ← 终端 搜索 / 录制 / 编码下拉宽度
1 @container (min-width:540px)     ← 导出到剪贴板 / 复制
1 @container (min-width:560px)     ← 命令块
1 @container (min-width:680px)     ← 凭据视图统计提示
0 条 @container (max-width:…)
```

### 3.3 `overflow-x` 带来的竖直裁切风险（已排除）

`overflow-x` 一旦不是 `visible`，`overflow-y` 会被计算成 `auto`，其内部
**绝对定位的弹层会被裁掉**。工具条里唯一的内联弹层是 `ContextMenu`，
而它是工具条的**兄弟节点**（挂在组件根部），不是子节点 ⇒ 不受影响。
这条已写进 `styles.css` 的注释，避免以后有人往工具条里塞 popover。

## 四、判据与结果

判据（全部来自 DOM 度量，不靠肉眼）：

- **竖排**：某个子项的文本盒 `lines > 1` 且高度 `> 40px`
- **右溢**：子项 `right` 超出工具条 `right` 超过 1px
- **横滚**：`bar.scrollWidth > bar.clientWidth + 1`
- **页面级**：`documentElement.scrollWidth == clientWidth`（页面本身不许横向溢出）

| # | 场景 | 面板宽 | 修复前 | 修复后 |
|---|---|---|---|---|
| ① | 凭据视图 1080×601（**用户截图场景**） | 422 | 竖排 2 项、`复制` 右溢 53px、需横滚(475>422) | 竖排 `[]`、右溢 `[]`、**不需横滚**(422=422) |
| ② | 凭据视图 1024×768 | 366 | 竖排 2 项、`导出`/`复制` 均右溢 | 竖排 `[]`、右溢 `[]`、横滚仅 9px(375>366) |
| ③ | 凭据视图 820×1180 | 162 | 竖排 2 项、6 项右溢 | 竖排 `[]`，退化为纯图标 + 横滚 |
| ④ | 终端 1080×601 | 422 | `终端 1` 13×58 竖排、**工具条高度被撑到 59px**、编码下拉被压到 42px | 竖排 `[]`、**不需横滚**、下拉 68px、条高回到 38px |
| ⑤ | 终端 1024×768 | 366 | `终端 1` 竖排、编码下拉被压到 **18px**、`命令块` 右溢 | 竖排 `[]`、右溢 `[]`、下拉 68px、横滚仅 6px |
| ⑥ | 设置 · 快捷键 1080×601 | 382 | **2 列**、`命令面板` 12×64 竖排(4 行)、`SQL 编辑器内运行` 折成 2 行 | **1 列**、全部单行、行高统一 16px |
| ⑦ | 设置 · 快捷键 1024×768 | 326 | **2 列**、5 项竖排 | **1 列**、竖排 `[]` |

7/7 通过；所有场景 `docOverflowX == 0`（页面从不横向溢出）。

> ③ 与 ⑤ 的"需横滚"是**设计内**的兜底行为，不是缺陷：横向滚动条已隐藏，
> 触控板 / 触屏可滑，触达性完好。阈值 367px 按实测自然宽度定，
> 低于它的只剩"主区窄过一部手机"的极端形态。

## 五、证据细节（截图见同目录 50–56）

### 5.1 凭据视图 1080×601（用户原报场景）

`50-narrow-cred-before-1080x601.png` vs `51-narrow-cred-after-1080x601.png`

修复前逐项：

```
svg                                  → 宽 0（被压没）
span       凭据视图        13 × 81    ← 一行一个字
span       只读            33 × 18
div        文本/JSON       125 × 28   （内部 5 个 line box）
span       7 条凭据 · …    23 × 110   ← 8 行
button     导出到剪贴板    107 × 25
button     复制            61 × 25    offRight +53  ⇒ 被裁
```

修复后：`凭据视图` 回到 **52 × 20（单行）**，三个右侧按钮统一 `32 × 25`（纯图标 + tooltip），
`offRight` 最大 `-12`（不溢出），`barScrollW == barClientW == 422`。

**已知的取舍**：该宽度下「只读」角标与统计提示被收掉。这是 §3.1 的优先级结果 ——
角标虽然优先级最低，但"全量 408px > 可用 398px"，无论如何塞不下；
强行显示只会让最右按钮被切 10px（比不显示更糟）。宽度回到 420px 容器以上即自动恢复。

### 5.2 终端工具条

`53-narrow-term-before-1024x768.png` vs `54-narrow-term-after-1024x768.png`

修复前 `终端 1` 13×58（3 行竖排）、工具条整体高度被撑到 59px（正常 38px）、
编码下拉 366px 下被压到 **18px**（`utf-8` 只剩一个 `u`）。修复后条高 38、
下拉 68px（`utf-8 ▾` 完整可见）、横滚仅 6px。

### 5.3 设置 · 快捷键卡片

`55-narrow-kbd-before-1080x601.png` vs `56-narrow-kbd-after-1080x601.png`

两列布局在 382px 卡片里把每格压到约 135px，而最长的一条 `Ctrl+Shift+P / Ctrl+K`
自己就占约 130px ⇒ 标签被挤成单字列。改为「默认 1 列 + `@min-[400px]:grid-cols-2`」后，
窄宽度自动落到单列、标签全部单行；**卡片反而更紧凑**（行高从被撑开的 64px 回到 16px）。

## 六、改动清单

| 文件 | 改动 |
|---|---|
| `src/styles.css` | `.nx-toolbar` 加 `container-type: inline-size` + `overflow-x:auto`；`.nx-toolbar-title` 加 `flex:0 0 auto; white-space:nowrap`（并新增可选 `.nx-ellipsize` 走省略号分支）；工具条内 `.nx-hint` / `.nx-select` 规则；新增 `.nx-hide-scrollbar` |
| `src/features/credentials/CredentialsView.tsx` | `@container` 工具条 + 三层降级 + 完整原因注释 |
| `src/features/credentials/CredentialsPanel.tsx` | 同套处理（`凭据视图` 按钮 `@min-[420px]`） |
| `src/features/terminal/TerminalPane.tsx` | 同套处理；编码下拉 `@max-` → `@min-` 反转并留警示注释 |
| `src/features/settings/SettingsView.tsx` | 快捷键卡片 `@container` + 网格改「1 列 → `@min-[400px]` 2 列」 |
| `src/features/files/FileEditor.tsx` | 长标题（文件路径）改用 `.nx-ellipsize` |
| `src/features/db/DbPanel.tsx` | 长标题（Redis 键名）改用 `.nx-ellipsize` |

后两个文件是**语义显式化**：原先靠 Tailwind 的 `truncate` 恰好生效，
现在改成"要省略号就显式挂 `.nx-ellipsize`"，避免以后有人给标题加了
`flex: 0 0 auto` 之后长路径被整条推出容器。

## 七、复现与回归手法

探针不是本次的交付物，但手法值得记：

```bash
# 1) 常驻 headless Chrome（必须用 run_in_background，否则命令结束即被回收）
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  --headless=new --no-sandbox --disable-gpu \
  --remote-debugging-port=9333 --remote-allow-origins='*' \
  --user-data-dir=/tmp/nx-cr-cdp about:blank

# 2) 窄视口一定走 Emulation.setDeviceMetricsOverride，
#    macOS headless 的窗口有 ~500px 最小宽度，--window-size 在窄宽度下不可靠
```

两个踩过的坑：

1. **启动期 ~400ms 不稳定窗口**：demo 模式下"自动连接当前设备"会把左导航强制切回
   文件树。探针必须 idle ≈2.5s 再开始点击，否则会出现重复标签之类的假象。
2. **整屏截图可能完全不含目标元素**：设置页的快捷键卡片在折叠线以下，
   前后两张整屏图**字节完全相同**（sha256 一致），差点被当成"没有差异"的证据。
   对有滚动位置的页面，要么 `scrollIntoView` 后按 `clip` 抓取，要么用 DOM 度量兜底。

## 八、遗留

- 同一批代码里另有一处**与本缺陷无关**、且经用户裁定**暂不修**的问题：
  同一 id 的标签可能同时出现在两个工作区（`addTab` 只在"解析出的工作区"内去重，
  `closeTab` 却扫全部工作区）。记录在
  `.workbuddy/memory/topics/10-architecture.md`，将来修必须两半一起补。
- 主区窄于 367px 时依赖横向滚动触达右侧按钮。当前无真实设备落在这个区间，
  暂不为它做第二套布局。
