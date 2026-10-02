// 演示模式：把全部 Tauri 命令映射到内存实现。
//
// 目的：让《产品开发文档》里的每个面板在**没有 Rust 内核、没有真实服务器**的情况下
// 都有内容可看、可点、可交互，从而在实现早期就能整体验收 UI。
//
// 接入点只有一处：`ipc/commands.ts` 的 `call()` 在演示模式下改走 `mockInvoke`，
// 因此所有面板代码一行都不用为演示模式做改动。

import {
  assets,
  auditEntries,
  containerStats,
  containers,
  conversations,
  conversationMessages,
  fakeQuery,
  forwards,
  fsFileContent,
  fsTree,
  groups,
  images,
  mounts,
  mysqlColumns,
  mysqlIndexes,
  mysqlSchemas,
  mysqlTables,
  modelState,
  permissionConfig,
  providerConfig,
  providerPresets,
  redisKeys,
  redisValues,
  uid,
} from "./data";
import { containerLogLines, DemoShell } from "./shell";
import { emit, later, pushEvent, pushText } from "./bus";

/* ── 会话 ─────────────────────────────────────────────────────────────── */

interface DemoSession {
  id: string;
  assetId: string | null;
  name: string;
  kind: string;
  status: string;
  tabs: string[];
  createdAt: number;
}

const sessions: DemoSession[] = [
  {
    id: "s-web01",
    assetId: "a-web01",
    name: "web-01",
    kind: "ssh",
    status: "connected",
    tabs: [],
    createdAt: Date.now() - 46 * 60_000,
  },
];

/* ── 终端 ─────────────────────────────────────────────────────────────── */

const shells = new Map<string, DemoShell>();
const logTimers = new Map<string, () => void>();
const pendingAi = new Map<string, (decision: string) => void>();
/** 演示模式的「停止」：被取消的 jobId 与它们的通道。 */
const cancelledAiJobs = new Set<string>();
const aiChannels = new Map<string, unknown>();
let jobSeq = 0;

/**
 * 演示模式的内核标签表（`terminal_list` / `terminal_attach_tab` 的数据源）。
 *
 * 关键点：**detach 不等于销毁**。关闭标签选「后台继续运行」时只把 subscribers 置 0，
 * 进程（DemoShell）留着 —— 这样「后台会话」面板才有的看、也才接得回来。
 * 之前 detach 直接 delete 掉 shell，等于把"关掉网页任务还在跑"这个核心场景
 * 在演示模式里演成了反面，功能在演示里根本验不到。
 */
interface DemoLiveTab {
  tabId: string;
  sessionId: string;
  sessionName: string;
  sessionKind: string;
  cols: number;
  rows: number;
  controller: string | null;
  subscribers: number;
  exited: boolean;
  lastOutputAt: number;
}

const liveTabs = new Map<string, DemoLiveTab>();

/** 预置一个「后台运行中」的标签：让「后台会话」面板一打开就有内容可看。 */
liveTabs.set("t-bg-demo", {
  tabId: "t-bg-demo",
  sessionId: "s-web01",
  sessionName: "web-01",
  sessionKind: "ssh",
  cols: 120,
  rows: 30,
  controller: null,
  subscribers: 0,
  exited: false,
  lastOutputAt: Date.now() - 42_000,
});

/** 演示模式的布局存储（`layout_get` / `layout_put`）。data 是前端自己的 JSON。 */
const layoutState: { revision: number; updatedAt: number; data: unknown | null } = {
  revision: 0,
  updatedAt: 0,
  data: null,
};

/**
 * 演示模式的错误：形状对齐内核的 `AppError`（`ipc/commands.ts::toAppError` 只看 `code`）。
 *
 * 不能 `throw new Error(...)` —— 那样 code 会退化成 `internal`，前端就分不出
 * 「别人正在操作终端」（not_controller）和真正的错误了。
 */
function throwAppError(code: string, message: string): never {
  throw { code, message };
}

/**
 * 演示「写文件」场景用的前后内容。
 *
 * 确认卡片的**改动预览**与执行后的**变更记录**必须用同一对常量：两处各写一份
 * 的话，演示本身就在演一个假的"前后一致"，而这个功能的价值恰恰是那份一致性。
 */
const DEMO_NGINX_PATH = "/etc/nginx/nginx.conf";
const DEMO_NGINX_BEFORE = "worker_processes 1;\nkeepalive_timeout 65;\nserver_tokens on;";
const DEMO_NGINX_AFTER =
  "worker_processes auto;\nkeepalive_timeout 65;\nserver_tokens off;\nclient_max_body_size 64m;";
/** 演示模式下"当前这条对话"的会话 id —— 镜像真机 `ai_chat` 的建会话/回传行为。 */
let liveConvId: string | undefined;

function newTabId(prefix = "t"): string {
  return `${prefix}-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
}

/** 演示模式下没有任何真实凭据，任何 secret 一律回显成掩码。 */
const MASK = "••••••••••••";

/* ── 凭据库（内存版，与真机 vault 命令一一对应）────────────────────────── */

interface DemoCredential {
  id: string;
  name: string;
  kind: string;
  /** 内容型：值本身 / 私钥正文；引用型私钥：本地文件路径。 */
  secret: string;
  /** 私钥专用：来源与口令（对齐真机凭据载荷里的 ref / passphrase 两个字段）。 */
  source?: "inline" | "file";
  passphrase?: string;
  createdAt: number;
  updatedAt: number;
}

/**
 * 演示凭据：secret 是假数据，可以随便显示/复制。
 * cred-dbprod 被两个资产共用（db-prod / build-01），用来演示引用关系与改值联动。
 */
const demoCredentials: DemoCredential[] = [
  { id: "cred-web01", name: "web-01", kind: "password", secret: "Xk9#web01$pw", createdAt: Date.now() - 20 * 86_400_000, updatedAt: Date.now() - 86_400_000 },
  { id: "cred-dbprod", name: "db-prod", kind: "password", secret: "Prod#db2026!", createdAt: Date.now() - 25 * 86_400_000, updatedAt: Date.now() - 3 * 86_400_000 },
  { id: "cred-nat", name: "nat-01", kind: "password", secret: "Nat0ld!2023", createdAt: Date.now() - 60 * 86_400_000, updatedAt: Date.now() - 30 * 86_400_000 },
  // 旧版的「独立口令凭据」留一条：验证兼容显示（新建入口已经不再提供这个类型）
  { id: "cred-old", name: "旧机房-口令", kind: "passphrase", secret: "old-machine-room", createdAt: Date.now() - 200 * 86_400_000, updatedAt: Date.now() - 90 * 86_400_000 },
  // 入库型私钥（带口令）：口令与私钥同一条凭据，详情页会有「私钥口令」卡片
  {
    id: "cred-key",
    name: "id_ed25519",
    kind: "private_key",
    source: "inline",
    passphrase: "demo-key-pass",
    secret:
      "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\nQyNTUxOQAAACCkZW1vb25seW5vdGFyZWFsa2V5MDAwMDAwMDAwMDAwMDAwMAAAAJgAAAAA\nAAAAAAAAAA==\n-----END OPENSSH PRIVATE KEY-----",
    createdAt: Date.now() - 45 * 86_400_000,
    updatedAt: Date.now() - 5 * 86_400_000,
  },
  // 引用型私钥：库里只有路径，正文不进库（演示「引用本地文件」这条来源）
  {
    id: "cred-key-ref",
    name: "id_rsa（引用）",
    kind: "private_key",
    source: "file",
    secret: "C:\\Users\\you\\.ssh\\id_rsa",
    createdAt: Date.now() - 70 * 86_400_000,
    updatedAt: Date.now() - 18 * 86_400_000,
  },
  {
    id: "cred-api",
    name: "shipyard-token",
    kind: "api_key",
    secret: "demo-token-not-real",
    createdAt: Date.now() - 12 * 86_400_000,
    updatedAt: Date.now() - 12 * 86_400_000,
  },
];

/**
 * 凭据库状态：默认「未启用密码保护」（dpapi、解锁）——对应重做后开关的默认关闭。
 * 在设置页打开保护 → 变 master；凭据页「立即锁定」/解锁会翻转 unlocked。
 */
const vaultState = {
  initialized: true,
  mode: "dpapi" as "dpapi" | "master",
  unlocked: true,
  autoLockMinutes: 30,
};

/* ── 参数归一 ─────────────────────────────────────────────────────────── */

/**
 * Rust 侧有的命令收 `args` 对象、有的收平铺参数（见 commands.ts）。
 * 这里统一取一层，避免每个 case 里都写 `a.args ?? a`。
 */
function params(raw: unknown): Record<string, unknown> {
  const o = (raw ?? {}) as Record<string, unknown>;
  const inner = o.args;
  if (inner && typeof inner === "object" && !Array.isArray(inner)) {
    return inner as Record<string, unknown>;
  }
  return o;
}

function str(v: unknown, fallback = ""): string {
  return typeof v === "string" ? v : fallback;
}
function num(v: unknown, fallback = 0): number {
  return typeof v === "number" && Number.isFinite(v) ? v : fallback;
}

/**
 * 把 SFTP 风格的路径规整成绝对路径。
 *
 * 左栏文件树的根是 `~`（真实后端由服务端展开），演示里我们自己展开到
 * 这台演示机的家目录，这样 `~` 与终端里的 `cd ~` 指向同一个地方。
 */
const DEMO_HOME = "/home/deploy";

function absPath(v: unknown): string {
  const raw = str(v, DEMO_HOME);
  if (raw === "~") return DEMO_HOME;
  if (raw.startsWith("~/")) return DEMO_HOME + raw.slice(1);
  return raw.replace(/\/+$/, "") || "/";
}

/**
 * 大文件的 base64：分块是为了不让 `String.fromCharCode(...bytes)` 展开超栈，
 * 但**块长必须是 3 的倍数** —— 否则除最后一块外每块结尾都带 `=` padding，
 * 拼起来就是一串非法 base64（解回来的内容从第一个块边界起全错）。
 */
const BASE64_CHUNK_BYTES = 32_766;

function bytesToBase64(bytes: Uint8Array): string {
  let b64 = "";
  for (let i = 0; i < bytes.length; i += BASE64_CHUNK_BYTES) {
    b64 += btoa(String.fromCharCode(...bytes.subarray(i, i + BASE64_CHUNK_BYTES)));
  }
  return b64;
}

/* ── AI 脚本 ──────────────────────────────────────────────────────────── */

function answerFor(question: string): { answer: string; commands: string[] } {
  const q = question.toLowerCase();
  if (q.includes("502") || q.includes("挂了") || q.includes("起不来") || q.includes("503")) {
    return {
      commands: ["docker ps --format '{{.Names}}\\t{{.Status}}'", "free -h", "docker logs --tail 20 api-server"],
      answer: [
        "定位到了，**不是 nginx 的问题**。",
        "",
        "1. `mysql-prod` 在 12 分钟前被 OOM Killer 干掉（退出码 137），`api-server` 连不上库，所以 Nginx 那边一直 502。",
        "2. 机器 7.7G 内存已被占满、swap 也快用尽，`api-server` 自己的容器日志里能看到 `pool exhausted` 与 `ECONNRESET`。",
        "3. 直接诱因是 `mysql-prod` 没有设置内存上限，叠加 `api-server` 的 1.4G 占用，触发了内核的 OOM 选择。",
        "",
        "建议按这个顺序处理：",
        "- `docker update --memory 2g api-server` — 先给 api-server 加上限，别再让它可以无限涨；",
        "- `docker start mysql-prod` — 拉起来，观察 5 分钟是否再次被杀；",
        "- 若反复被杀：把 `innodb_buffer_pool_size` 从 1.4G 降到 512M，或给机器加 swap。",
        "",
        "| 容器 | 状态 | 说明 |",
        "| --- | --- | --- |",
        "| `mysql-prod` | 已退出 (137) | 被 OOM Killer 干掉 |",
        "| `api-server` | 运行中 | 连不上库 → 对外一直 502 |",
        "| `redis` | 运行中 | 内存 48M / 上限 512M，健康 |",
      ].join("\n"),
    };
  }
  if (q.includes("内存") || q.includes("memory") || q.includes("oom")) {
    return {
      commands: ["free -h", "ps aux --sort=-%mem | head -8"],
      answer: [
        "内存确实到顶了。",
        "",
        "- 物理内存 7.7G / 已用 7.3G / 可用仅 380Mi；swap 2G 也用了 1.9G。",
        "- 占用前两名：`mysqld` 1.42G（18.4%）、`node dist/server.js` 412M（5.1%）。",
        "",
        "结论：这台机器现在的余量撑不住任何一次内存尖峰，建议先把 `mysql-prod` 的 buffer pool 降下来。",
      ].join("\n"),
    };
  }
  if (q.includes("redis")) {
    return {
      commands: ["docker exec redis-cache redis-cli info memory"],
      answer: [
        "`redis-cache` 状态健康。",
        "",
        "- `maxmemory` 已设为 512mb、策略 `allkeys-lru`，当前用了 48.2M。",
        "- 命中率 97.4%，没有明显的缓存击穿迹象。",
        "",
        "这块暂时不用动。",
      ].join("\n"),
    };
  }
  if (q.includes("nginx") || q.includes("配置")) {
    return {
      commands: ["nginx -t", "cat /etc/nginx/nginx.conf"],
      answer: [
        "nginx 配置本身没问题。",
        "",
        "- `nginx -t` 通过，语法与 include 路径都正常。",
        "- `/api/` 反代到上游 `api_backend`（127.0.0.1:8080），`proxy_read_timeout 60s`。",
        "",
        "502 是上游给的，不是 nginx 的问题 —— 建议把注意力放在后端容器上。",
      ].join("\n"),
    };
  }
  return {
    commands: ["uptime", "df -h"],
    answer: [
      "我看了下这台机器的概况：",
      "",
      "- 已连续运行 87 天，load average 2.14 / 3.02 / 2.66，比 8 核的期望值偏高一些但不算异常。",
      "- 根分区 99G 用了 68%，`/data` 197G 用了 77%，都还有余量。",
      "- 三个容器在跑，`nginx-gateway` :80/:443、`api-server` :8080、`redis-cache` :6379。",
      "",
      "你想从哪一块往下看？容器、数据库还是 nginx 配置？",
    ].join("\n"),
  };
}

function streamAnswer(rawChannel: unknown, jobId: string, question: string, planMode = false) {
  // 取消闸门。
  //
  // 真机上是 `CancellationToken`，演示模式没有那个东西 —— 但「点了停止到底
  // 有没有用」必须在演示里也看得见，否则这个按钮等于没人验过。这里把 channel
  // 包一层：jobId 一旦进取消表，后续事件全部丢弃（等价于内核那边 break 掉整个循环）。
  // 选择包 channel 而不是逐个改 `pushEvent(channel, …)`，是为了函数体一行都不用动。
  const channel = {
    onmessage: (evt: Record<string, unknown>) => {
      if (cancelledAiJobs.has(jobId)) return;
      pushEvent(rawChannel, evt);
    },
    toJSON: () => null,
  };
  const { answer, commands } = answerFor(question);
  const yesNo = question.includes("重启") || question.includes("restart") || question.includes("删除");

  /**
   * 这一轮最终的完整回答 —— 流式和 `done` 用的是**同一个字符串**。
   *
   * 真机就是这么干的：整段正文都是流式（`delta`）推出去的，最后 `done` 再把同一份
   * 完整文本回传一次。演示以前给 `done` 塞的是另一段文字（收尾的代码块），
   * 于是「同一段回答在对话里出现两遍」这个真机 bug 在演示模式里**永远复现不出来** ——
   * mock 越是"自成一派"，越容易把真机的问题挡在门外。
   */
  const cmdsBlock = commands.length
    ? "```bash\n" + commands.map((c) => `$ ${c}`).join("\n") + "\n```\n"
    : "";
  const fullAnswer = `${answer}\n\n${cmdsBlock}—— 以上命令都已在你面前的终端里跑过，可回放。`;

  // 思考片段先推完再开始跑工具，让"推理是否被合并"这件事在界面上看得清
  let delay = 430;

  // 上下文用量圆环：真机上每轮 LLM 调用后都会推一次，这里固定一份可读的数
  later(140, () =>
    pushEvent(channel, {
      type: "usage",
      promptTokens: 8420,
      completionTokens: 386,
      cachedTokens: 6016,
      contextWindow: 64000,
    }),
  );

  // 演示「思考过程」：真机上推理模型是**逐 token** 流式推的，这里故意拆成很多
  // 小片段 —— 前端要是每条都新开一个气泡，就会变成"几个字一行"。
  const thinking = ["先看容器状态", "，确认是不是进程", "没了；", "再查内存", "，", "OOM 的可能性", "最大。"];
  thinking.forEach((t, i) =>
    later(70 + i * 45, () => pushEvent(channel, { type: "reasoning", text: t })),
  );

  // 计划模式：只出方案、一个字都不动 —— 与内核行为一致
  if (planMode) {
    const plan = [
      "## 目标",
      "重启 mysql-prod，尽量缩短服务中断时间。",
      "",
      "## 步骤",
      "1. 确认 mysql-prod 当前状态与最近日志（只读）",
      "2. 检查 /data 剩余空间（只读）",
      "3. docker restart mysql-prod",
      "4. 复查容器状态与健康检查",
      "",
      "## 风险",
      "- 第 3 步会中断服务约 5–15 秒",
      "- 若第 4 步未通过，需要回滚到上一次镜像",
    ].join("\n");
    later(360, () => pushEvent(channel, { type: "planSubmitted", plan }));
    later(560, () => pushEvent(channel, { type: "done", answer: plan }));
    return;
  }

  // 写文件场景：演示「确认卡片在批准之前就给出逐行 diff」+ 执行后的变更记录。
  //
  // 这条路径必须留在演示里：没有本地模型的机器上，这是唯一能看到该功能的地方
  // （本轮改动的原因就是"新建/写入的改动记录在真机上看不到"）。
  if (/写|新建|保存|创建|改一下|加一行/.test(question)) {
    const callId = `call-${uid("c")}`;
    // 新建与修改两条都要能演：**新建文件**正是本轮修掉的那个缺口
    // （文件原本不存在时，以前整条变更记录都不推）。只演修改等于把 bug 藏起来。
    const creating = /新建|创建/.test(question);
    const path = creating ? "/etc/nginx/conf.d/upload.conf" : DEMO_NGINX_PATH;
    const before = creating ? "" : DEMO_NGINX_BEFORE;
    const after = creating
      ? "client_max_body_size 64m;\nproxy_read_timeout 120s;\n"
      : DEMO_NGINX_AFTER;
    const verb = creating ? "新建" : "修改";

    later(400, () => pushEvent(channel, { type: "status", phase: "thinking", turn: 1 }));
    // 模型把整份内容当成工具参数逐 token 吐出来 —— 这一段在真机上最长，
    // 而 2026-09-30 之前内核在这期间**一个字都不往外说**：界面在 AI 说完
    // 开场白之后完全静止几十秒，用户合理地把它读成卡死。演示必须留出这段，
    // 否则「写入过程有反馈」在没有本地模型的机器上根本验不到。
    // 间隔对齐内核的节流口径（120ms），不是随手取的数。
    [0.25, 0.5, 0.75, 1].forEach((frac, i) => {
      later(460 + i * 110, () =>
        pushEvent(channel, {
          type: "toolArgs",
          tool: "write_file",
          chars: Math.round(after.length * frac),
        }),
      );
    });
    later(920, () =>
      pushEvent(channel, {
        type: "toolCall",
        id: callId,
        name: "write_file",
        display: `写入 ${path}`,
      }),
    );
    later(1040, () =>
      pushEvent(channel, {
        type: "confirmRequired",
        id: callId,
        tool: "write_file",
        // 真机这里塞的是 `工具名 + 原始 JSON 参数 + 理由`，前端只在没有 preview
        // 时才展示它 —— 演示也照这个形状给，免得两边字段对不上。
        rendered: `write_file {"path":"${path}","content":"…"}\n这是本会话第 1 次请求写权限。`,
        reason: "这是本会话第 1 次请求写权限。",
        preview: { path, kind: creating ? "create" : "modify", before, after },
      }),
    );
    pendingAi.set(jobId, (decision) => {
      if (decision === "deny") {
        pushEvent(channel, {
          type: "toolResult",
          id: callId,
          ok: false,
          summary: "用户拒绝了该操作",
          text: "用户拒绝了该操作。",
          exitCode: null,
        });
        const text = "已取消，没有动任何文件。";
        pushEvent(channel, { type: "delta", text });
        finish(text);
        return;
      }
      pushEvent(channel, {
        type: "toolResult",
        id: callId,
        ok: true,
        summary: `写入 ${path}（${after.length} 字节）`,
        text: creating
          ? `写入 ${path} 成功（${after.length} 字节）。新文件，没有可备份的原内容。`
          : `写入 ${path} 成功（${after.length} 字节）。原文件已备份为 ${path}.nexterm-bak`,
        exitCode: null,
      });
      // 变更记录：与上面确认卡片里那份预览**同源**，两边显示不一致就是这个功能坏了。
      pushEvent(channel, { type: "fileChange", id: callId, path, before, after });
      const text = creating
        ? [`已${verb} \`${path}\`：`, "", "- 整份都是新增内容（原文件不存在）", "", "改错了直接删掉这个文件即可。"].join("\n")
        : [
            `已按你的授权改掉 \`${path}\` 的两处配置：`,
            "",
            "- `worker_processes 1` → `auto`",
            "- `server_tokens on` → `off`（不再对外报版本号）",
            "- 新增 `client_max_body_size 64m`",
            "",
            "要回滚就用同目录的 `.nexterm-bak`。",
          ].join("\n");
      pushEvent(channel, { type: "delta", text });
      finish(text);
    });
    return;
  }

  // 真机每轮 LLM 调用前都会推 `status`（"第 N 轮 · 正在思考…"）。
  // 这个事件前端一直没渲染，界面在 AI 跑长命令时完全静止 ——
  // 「卡住了」的焦虑有一半来自这里。演示也推一份，否则状态条永远验不到。
  later(400, () => pushEvent(channel, { type: "status", phase: "thinking", turn: 1 }));

  commands.forEach((cmd) => {
    later(delay, () => pushEvent(channel, { type: "toolCall", id: `call-${uid("c")}`, name: "exec_commands", display: cmd }));
    delay += 520;
  });

  later(delay, () => {
    if (yesNo) {
      pushEvent(channel, {
        type: "confirmRequired",
        id: `call-${uid("c")}`,
        tool: "docker_control",
        rendered: `docker restart mysql-prod\n\n影响：服务将中断约 5–15 秒。\n这是本会话第 1 次请求写权限。`,
        reason: "这是本会话第 1 次请求写权限。",
        // 重启容器不是写文件：没有前后对照可言，卡片走原始参数那条路。
        preview: null,
      });
      pendingAi.set(jobId, (decision) => {
        // 拼出"这一轮最终回答"，再把它当成 done 的 answer —— 与真机一致：
        // 流式推出去的那段就是 done 回传的那段。
        const head = decision === "deny" ? "" : "已按你的授权执行 `docker restart mysql-prod`，容器已重启：\n\n";
        const body =
          decision === "deny"
            ? "已取消，没有执行任何写操作。"
            : commands.map((c) => `  $ ${c}\n`).join("") + "\n";
        if (head) pushEvent(channel, { type: "delta", text: head });
        pushEvent(channel, { type: "delta", text: body });
        finish(head + body);
      });
      return;
    }
    commands.forEach((cmd, i) => {
      // 真机推的是**两份**：summary 是 text 的 400 字截断，text 是完整输出。
      // 演示里也必须分成两份 —— 否则「展开完整输出」永远不出现，
      // 等于这个功能在演示模式下没人验得到（这个教训我们已经吃过一次了）。
      const output = `$ ${cmd}\n${toolOutputFor(cmd)}`;
      const summary = output.length > 400 ? `${output.slice(0, 400)}…` : output;
      later(240 + i * 260, () =>
        pushEvent(channel, {
          type: "toolResult",
          summary,
          text: output,
          ok: true,
          exitCode: 0,
        }),
      );
    });
    delay = 240 + commands.length * 260 + 200;
    // 工具跑完、开始组织回答的那一轮 —— 状态条跟着翻到第 2 轮，
    // 让人看得出"它在往下走"，而不是停在同一句话上不动。
    later(delay - 160, () =>
      pushEvent(channel, { type: "status", phase: "thinking", turn: 2 }),
    );
    // 演示「变更记录」：AI 改过文件时，会话里会出现这样一张 diff 卡片
    later(200 + commands.length * 260, () =>
      pushEvent(channel, {
        type: "fileChange",
        id: `call-${uid("c")}`,
        path: DEMO_NGINX_PATH,
        before: DEMO_NGINX_BEFORE,
        after: DEMO_NGINX_AFTER,
      }),
    );
    // 演示「任务清单」：长任务里 AI 会边做边更新，清单钉在输入区上方
    if (/排查|部署|安装|迁移|优化|重构/.test(question)) {
      later(160 + commands.length * 260, () =>
        pushEvent(channel, {
          type: "todos",
          items: [
            { content: "确认目标主机与当前状态", status: "completed" },
            { content: "采集运行数据（CPU / 内存 / 磁盘）", status: "completed" },
            { content: "定位瓶颈并给出方案", status: "in_progress" },
            { content: "执行修复并复查", status: "pending" },
          ],
        }),
      );
    }
    answer.split("\n").forEach((line, i) => {
      later(delay + i * 70, () => pushEvent(channel, { type: "delta", text: `${line}\n` }));
    });
    later(delay + answer.split("\n").length * 70 + 120, () => finish(fullAnswer));
  });

  function finish(finalAnswer: string) {
    later(0, () => {
      emit("session://status", { sessionId: "s-web01", status: "connected", error: null });
      pushEvent(channel, { type: "done", answer: finalAnswer });
    });
  }
}

function toolOutputFor(cmd: string): string {
  if (cmd.startsWith("docker ps")) {
    return containers
      .map((c) => `${c.name.padEnd(16)}${c.status}`)
      .join("\n");
  }
  if (cmd.startsWith("free")) {
    return "               total        used        free      shared  buff/cache   available\nMem:           7.7Gi       7.3Gi       380Mi        12Mi       189Mi       172Mi\nSwap:          2.0Gi       1.9Gi       104Mi";
  }
  if (cmd.startsWith("docker logs")) {
    // 推**全量**日志（真机里 summary 是 400 字截断、text 是全文）——
    // 演示模式得有一条明显超过 400 字的输出，「展开完整输出」才看得出价值。
    return containerLogLines("api-server").join("\n");
  }
  if (cmd.startsWith("docker exec redis")) {
    return "used_memory:50544640\nused_memory_human:48.20M\nmaxmemory:536870912\nmaxmemory_policy:allkeys-lru";
  }
  if (cmd.startsWith("nginx -t")) {
    return "nginx: configuration file /etc/nginx/nginx.conf test is successful";
  }
  if (cmd.startsWith("cat /etc/nginx")) {
    // 全文（49 行 ≈ 1.4K 字符）：折叠态只给前 400 字，展开才看得到
    // 那个 `proxy_read_timeout` 之下的部分 —— 演示「展开」用最直观的一条。
    return fsFileContent["/etc/nginx/nginx.conf"];
  }
  if (cmd.startsWith("uptime")) {
    return " 16:31:02 up 87 days,  3:41,  2 users,  load average: 2.14, 3.02, 2.66";
  }
  if (cmd.startsWith("df")) {
    return "Filesystem      Size  Used Avail Use% Mounted on\n/dev/vda1        99G   63G   31G  68% /\n/dev/vdb1       197G  142G   45G  77% /data";
  }
  if (cmd.startsWith("ps aux")) {
    return "USER         PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND\nmysql       1421 12.3 18.4 2034892 1483120 ?  Ssl  09:12 118:02 mysqld\ndeploy      1183 24.7  5.1 934297 412180 ?     Ssl  09:12  62:14 node dist/server.js";
  }
  return "（演示模式：已省略该命令的完整输出）";
}

/* ── 主分发 ───────────────────────────────────────────────────────────── */

export async function mockInvoke(cmd: string, rawArgs?: Record<string, unknown>): Promise<unknown> {
  const a = params(rawArgs);
  // `params` 只把 `args` 摊平，**顶层的兄弟键会一起丢掉** —— 而 Tauri 的 Channel
  // 必须走顶层（塞不进结构体），于是 `ai_chat` 的 channel 会被吃掉、AI 事件
  // 一个都推不出去（现象是发完消息界面毫无反应）。这里补回来。
  if (rawArgs && typeof rawArgs === "object") {
    const top = rawArgs as Record<string, unknown>;
    if ("channel" in top && !("channel" in a)) a.channel = top.channel;
  }
  // 所有命令都加一点点延迟，模拟 IPC 往返；也让 loading 态可见
  await new Promise((r) => window.setTimeout(r, 40 + Math.random() * 60));

  switch (cmd) {
    /* ─────────────── session ─────────────── */
    case "session_list":
      return sessions.map((s) => ({ ...s }));

    case "session_connect":
    case "session_connect_local": {
      const assetId = cmd === "session_connect_local" ? "a-local" : str(a.assetId);
      const asset = assets.find((x) => x.id === assetId) ?? assets[0];
      const existing = sessions.find((s) => s.assetId === asset.id);
      if (existing) {
        existing.status = "connected";
        return { ...existing };
      }
      const s: DemoSession = {
        id: uid("s"),
        assetId: asset.id,
        name: asset.name,
        kind: asset.kind,
        status: "connecting",
        tabs: [],
        createdAt: Date.now(),
      };
      sessions.push(s);
      later(420, () => {
        s.status = "connected";
        emit("session://status", { sessionId: s.id, status: "connected", error: null });
      });
      return { ...s };
    }

    case "session_disconnect": {
      const i = sessions.findIndex((s) => s.id === str(a.sessionId));
      if (i >= 0) {
        const s = sessions[i];
        // 必须跟内核同语义，否则演示里验不出真实行为：
        //   有重连语义的（ssh/docker/winrm）→ **对象留着**，只置 disconnected
        //     （「重新连接」要靠它重建传输，删了那个按钮就永远点不动）
        //   没有的（local 等）→ 断开就是结束，彻底摘掉
        if (s.kind === "ssh" || s.kind === "docker" || s.kind === "winrm") {
          s.status = "disconnected";
          s.tabs = [];
        } else {
          sessions.splice(i, 1);
        }
        // 不发这条事件的话，前端的状态徽标会一直停在「已连接」，
        // 于是「重新连接」永远是禁用态 —— 演示模式里这条路径就验不了。
        emit("session://status", { sessionId: s.id, status: "disconnected", error: null });
      }
      return null;
    }

    case "session_probe":
      return { open: true };

    case "session_reconnect": {
      // 内核侧是非阻塞的：马上返回 true（已开始），结果靠状态事件推。
      // 演示模式没有真实传输层，就演一遍状态机：connecting → connected。
      const s = sessions.find((x) => x.id === str(a.sessionId));
      // 跟内核一样，先判"有没有重连这回事"：本机会话与没绑定资产的都不可重连
      if (!s || !s.assetId || s.kind === "local") return false;
      s.status = "connecting";
      emit("session://status", { sessionId: s.id, status: "connecting", error: null });
      later(600, () => {
        s.status = "connected";
        emit("session://status", { sessionId: s.id, status: "connected", error: null });
      });
      return true;
    }

    case "session_cwd":
      return "/data/app";

    case "session_open_line_tab": {
      const tabId = newTabId("line");
      const ch = a.channel;
      pushText(ch, "\r\n\x1b[33m[WinRM 非交互行模式] 每条命令在新的 shell 中执行，不支持 vim/top 等 TUI\x1b[0m\r\n");
      pushText(ch, "PS C:\\Users\\Administrator> ");
      return tabId;
    }

    case "session_line_exec": {
      const line = str(a.line);
      // WinRM 通道没有对应的 channel，这里从最近的 tab 找不到就只回显
      void line;
      return null;
    }

    /* ─────────────── terminal ─────────────── */
    case "terminal_attach": {
      const tabId = newTabId("t");
      const sessionId = str(a.sessionId);
      const sess = sessions.find((s) => s.id === sessionId);
      const shell = new DemoShell((text) => pushText(a.channel, text.replace(/\n/g, "\r\n")));
      shells.set(tabId, shell);
      liveTabs.set(tabId, {
        tabId,
        sessionId,
        sessionName: sess?.name ?? "web-01",
        sessionKind: sess?.kind ?? "ssh",
        cols: num(a.cols, 120),
        rows: num(a.rows, 30),
        // 新建时先来的人自动成为操作者（对齐内核 open_terminal_tab 的语义）
        controller: str(a.clientId) || "desktop",
        subscribers: 1,
        exited: false,
        lastOutputAt: Date.now(),
      });
      const asset = assets.find((x) => x.id === sess?.assetId);
      later(90, () => {
        pushText(a.channel, `\r\n\x1b[2m[演示模式] 已连到 ${asset?.name ?? "web-01"}（假数据，随便敲）\x1b[0m\r\n\r\n`);
        shell.start();
      });
      return tabId;
    }

    case "terminal_attach_tab": {
      // 接管已有标签：不新开 shell。未知 tabId 报 not_found —— 和内核一致，
      // 界面会给一句「[会话已结束]」的提示，而不是静默留一个空终端。
      const tabId = str(a.tabId);
      const lt = liveTabs.get(tabId);
      if (!lt) throwAppError("not_found", "终端标签不存在（可能进程已结束）");
      const client = str(a.clientId) || "desktop";
      // 订阅者 +1；无人持权时先来的人自动成为操作者（对齐 attach_existing_tab 的 claim_if_free）
      lt.subscribers += 1;
      if (!lt.controller) lt.controller = client;
      lt.lastOutputAt = Date.now();
      // 演示模式没有真正的回滚缓冲，用一个绑到新通道的 shell 顶替，
      // 保证接管后能继续敲（不然只能看到一行横幅、字打不进去）。
      const shell = new DemoShell((text) => pushText(a.channel, text.replace(/\n/g, "\r\n")));
      shells.set(tabId, shell);
      later(70, () => {
        pushText(a.channel, `\x1b[2m[演示模式] 已接回后台终端（回放最近的输出）\x1b[0m\r\n\r\n`);
        shell.start();
      });
      return {
        tabId: lt.tabId,
        sessionId: lt.sessionId,
        cols: lt.cols,
        rows: lt.rows,
        controller: lt.controller,
        subscribers: lt.subscribers,
        exited: lt.exited,
      };
    }

    case "terminal_write": {
      const tabId = str(a.tabId);
      const lt = liveTabs.get(tabId);
      const client = str(a.clientId);
      // 单点模式：别人持权时拒绝。前端据此切观察者态（不弹错误框）。
      if (lt && client && lt.controller && lt.controller !== client) {
        throwAppError("not_controller", "终端正在其他设备上操作中");
      }
      const shell = shells.get(tabId);
      const data = a.data;
      if (shell && Array.isArray(data)) {
        shell.input(new TextDecoder().decode(Uint8Array.from(data as number[])));
        if (lt) lt.lastOutputAt = Date.now();
      }
      return null;
    }

    case "terminal_resize": {
      const lt = liveTabs.get(str(a.tabId));
      const client = str(a.clientId);
      if (lt && client && lt.controller && lt.controller !== client) {
        throwAppError("not_controller", "终端正在其他设备上操作中");
      }
      if (lt) {
        lt.cols = num(a.cols, lt.cols);
        lt.rows = num(a.rows, lt.rows);
      }
      return null;
    }

    case "terminal_resize_flush": {
      const lt = liveTabs.get(str(a.tabId));
      const client = str(a.clientId);
      if (lt && client && lt.controller && lt.controller !== client) {
        throwAppError("not_controller", "终端正在其他设备上操作中");
      }
      return null;
    }

    case "terminal_claim": {
      const lt = liveTabs.get(str(a.tabId));
      const client = str(a.clientId) || "desktop";
      if (!lt) return null;
      const prev = lt.controller;
      lt.controller = client;
      return prev === client ? null : prev;
    }

    case "terminal_release": {
      const lt = liveTabs.get(str(a.tabId));
      const client = str(a.clientId) || "desktop";
      if (!lt || lt.controller !== client) return false;
      lt.controller = null;
      return true;
    }

    case "terminal_list":
      return Array.from(liveTabs.values()).map((lt) => ({
        tabId: lt.tabId,
        sessionId: lt.sessionId,
        sessionName: lt.sessionName,
        sessionKind: lt.sessionKind,
        cols: lt.cols,
        rows: lt.rows,
        controller: lt.controller,
        subscribers: lt.subscribers,
        exited: lt.exited,
        lastOutputMsAgo: Date.now() - lt.lastOutputAt,
      }));

    case "terminal_set_visible":
    case "terminal_switch_encoding":
      return null;

    case "terminal_detach": {
      // 只摘订阅，**进程留着**：这正是「关掉网页任务还在跑」的语义。
      const lt = liveTabs.get(str(a.tabId));
      if (lt) {
        lt.subscribers = Math.max(0, lt.subscribers - 1);
        if (lt.subscribers === 0 && lt.controller === str(a.clientId)) lt.controller = null;
      }
      return null;
    }

    case "terminal_close_tab": {
      const tabId = str(a.tabId);
      if (str(a.mode) === "detach") {
        // 后台继续运行：从视图拿走，进程留在服务端（能在「后台会话」里接回来）
        const lt = liveTabs.get(tabId);
        if (lt) {
          lt.subscribers = 0;
          lt.controller = null;
        }
        return null;
      }
      shells.delete(tabId);
      liveTabs.delete(tabId);
      logTimers.get(tabId)?.();
      logTimers.delete(tabId);
      return null;
    }

    case "terminal_screen_text":
      return "deploy@web-01:/data/app$ docker ps\napi-server   Up 3 hours\nredis-cache  Up 3 hours (healthy)\nmysql-prod   Exited (137) 12 minutes ago";

    case "terminal_snapshot":
      return {
        text: "deploy@web-01:/data/app$ ",
        lines: ["deploy@web-01:/data/app$ "],
        cursorRow: 0,
        cursorCol: 25,
        cols: num(a.cols, 120),
        rows: num(a.rows, 30),
        altScreen: false,
        lastOutputMsAgo: 380,
      };

    case "terminal_tail":
      return [
        "deploy@web-01:~$ systemctl status nginx",
        "● nginx.service - A high performance web server and a reverse proxy server",
        "     Active: active (running) since Sat 2026-09-26 09:12:04 CST; 7h ago",
        "deploy@web-01:~$ docker ps --format '{{.Names}}\\t{{.Status}}'",
        "api-server      Up 3 hours",
        "mysql-prod      Exited (137) 12 minutes ago",
      ];

    case "terminal_dump":
      return "deploy@web-01:/data/app$ \n";

    case "terminal_record_start":
      return null;

    case "terminal_record_stop":
      return 184_320;

    case "terminal_export_log":
      // 演示模式不落盘，按回滚缓冲的字符数回一个"字节数"
      return 4096;

    /* ─────────────── layout ─────────────── */
    case "layout_get":
      return {
        revision: layoutState.revision,
        updatedAt: layoutState.updatedAt,
        data: layoutState.data,
      };

    case "layout_put": {
      // 乐观锁：revision 对不上就是对端在我们之后改过 → conflict，**不覆盖**
      // （布局没有可合并语义，前端拿到 conflict 会去拉最新）。
      if (num(a.revision, 0) !== layoutState.revision) {
        return { saved: false, revision: layoutState.revision, conflict: true };
      }
      try {
        layoutState.data = JSON.parse(str(a.data, "null"));
      } catch {
        layoutState.data = null;
      }
      layoutState.revision += 1;
      layoutState.updatedAt = Date.now();
      // 事件**延迟一帧**发：真机上事件走 WS，一定晚于 RPC 的响应。
      // 同步发的话会早于前端更新本地 revision，自己收到自己的事件、
      // 触发一次多余的拉取（真机上不会发生）—— 演示也就演不出回声判据了。
      const rev = layoutState.revision;
      later(0, () => emit("layout://changed", { revision: rev }));
      return { saved: true, revision: rev, conflict: false };
    }

    /* ─────────────── asset / group / snippet ─────────────── */
    case "asset_list":
      return assets.filter((x) => x.deletedAt === null).map((x) => ({ ...x }));

    case "asset_get":
      return { ...(assets.find((x) => x.id === str(a.id)) ?? assets[0]) };

    case "asset_search": {
      const q = str(a.q).toLowerCase();
      return assets
        .filter(
          (x) =>
            x.deletedAt === null &&
            (x.name.toLowerCase().includes(q) ||
              (x.host ?? "").toLowerCase().includes(q) ||
              (x.username ?? "").toLowerCase().includes(q)),
        )
        .map((x) => ({ ...x }));
    }

    case "asset_create": {
      const created = {
        id: uid("a"),
        groupId: (a.groupId as string | null) ?? null,
        kind: str(a.kind, "ssh"),
        name: str(a.name, "新资产"),
        host: (a.host as string | null) ?? null,
        port: (a.port as number | null) ?? null,
        username: (a.username as string | null) ?? null,
        authKind: (a.authKind as string | null) ?? "password",
        keyPath: (a.keyPath as string | null) ?? null,
        credId: (a.credId as string | null) ?? null,
        options: (a.options as Record<string, unknown> | null) ?? {},
        tags: "",
        note: "",
        sort: 99,
        createdAt: Date.now(),
        updatedAt: Date.now(),
        deletedAt: null,
        builtin: false,
      };
      assets.push(created);
      return { ...created };
    }

    case "asset_save_key_file":
      return { path: `C:\\Users\\you\\.ssh\\nexterm-${uid("key")}.pem` };

    case "asset_read_key_file":
      return "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAAG1lcnRpbi1uYW0AAAAA\n-----END OPENSSH PRIVATE KEY-----\n";

    case "asset_update": {
      const target = assets.find((x) => x.id === str(a.id));
      if (target) Object.assign(target, a, { updatedAt: Date.now() });
      return { ...(target ?? assets[0]) };
    }

    case "asset_delete": {
      const target = assets.find((x) => x.id === str(a.id));
      // 与内核一致：内置的「当前设备」拒绝删除（UI 已经不给按钮，
      // 但演示模式也得守住这条，否则"演示里能删、装上去删不掉"）。
      if (target?.builtin) throw new Error("「当前设备」是内置资产，不能删除");
      if (target) target.deletedAt = Date.now();
      return null;
    }

    case "group_list":
      return groups.map((g) => ({ ...g }));

    case "group_create": {
      const g = {
        id: uid("g"),
        parentId: (a.parentId as string | null) ?? null,
        name: str(a.name, "新分组"),
        sort: 99,
        createdAt: Date.now(),
        updatedAt: Date.now(),
      };
      groups.push(g);
      return { ...g };
    }

    case "group_update": {
      const g = groups.find((x) => x.id === str(a.id));
      if (g) {
        if (typeof a.name === "string") g.name = a.name;
        Object.assign(g, { updatedAt: Date.now() });
      }
      return { ...(g ?? groups[0]) };
    }

    case "group_delete": {
      const i = groups.findIndex((x) => x.id === str(a.id));
      if (i >= 0) groups.splice(i, 1);
      return null;
    }

    case "snippet_list":
      return [
        { id: "sn1", groupId: null, name: "看容器状态", body: "docker ps --format '{{.Names}}\\t{{.Status}}'", sort: 1 },
        { id: "sn2", groupId: null, name: "磁盘水位", body: "df -h && du -sh /data/*", sort: 2 },
        { id: "sn3", groupId: null, name: "nginx 重载", body: "sudo nginx -t && sudo nginx -s reload", sort: 3 },
      ];

    case "snippet_create":
      return { id: uid("sn") };

    case "snippet_update":
    case "snippet_delete":
      return null;

    case "audit_query": {
      const source = str(a.source);
      const limit = num(a.limit, 300);
      return auditEntries.filter((e) => !source || e.source === source).slice(0, limit);
    }

    case "known_host_list":
      return [
        { id: "kh1", host: "127.0.0.1", port: 22, keyType: "ssh-ed25519", fingerprint: "SHA256:9xKq7mP2vL4nR8sT1uW3yA5bC6dE7fG8hI9jK0lM1nO", addedAt: Date.now() - 40 * 86_400_000 },
        { id: "kh2", host: "10.0.0.8", port: 22, keyType: "ssh-rsa", fingerprint: "SHA256:2bN4cD6eF8gH0iJ2kL4mN6oP8qR0sT2uV4wX6yZ8aB0c", addedAt: Date.now() - 30 * 86_400_000 },
      ];

    case "known_host_accept":
    case "known_host_remove":
      return null;

    case "app_info":
      return {
        name: "NexTerm",
        version: "0.1.0-demo",
        vault: { initialized: true, mode: "master", unlocked: true, autoLockMinutes: 30 },
      };

    /* ─────────────── fs ─────────────── */
    case "fs_list": {
      const path = absPath(a.path);
      return (fsTree[path] ?? []).map((e) => ({ ...e }));
    }

    case "fs_read": {
      const path = absPath(a.path);
      const text = fsFileContent[path] ?? `# ${path}\n\n（演示模式：这个文件没有内置内容，随便改都行）\n`;
      const bytes = new TextEncoder().encode(text);
      return { path, size: bytes.length, contentBase64: bytesToBase64(bytes) };
    }

    case "fs_write": {
      const path = absPath(a.path);
      const b64 = str(a.contentBase64);
      let bin: Uint8Array;
      try {
        bin = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
      } catch {
        // 与内核一致：坏输入要报错，不能假装写成功（否则界面显示已保存而内容没变）
        throwAppError("bad_param", "contentBase64 不是合法的 base64，写入已中止");
      }
      let text: string;
      try {
        // 演示按文本存文件：fatal 解码，拒绝把非法 UTF-8 静默替换成 U+FFFD 后"写成功"
        text = new TextDecoder("utf-8", { fatal: true }).decode(bin);
      } catch {
        throwAppError("bad_param", "内容不是合法的 UTF-8 文本，写入已中止");
      }
      fsFileContent[path] = text;
      return null;
    }

    case "fs_mkdir": {
      const path = absPath(a.path);
      const parent = path.slice(0, path.lastIndexOf("/")) || "/";
      const name = path.slice(path.lastIndexOf("/") + 1);
      if (!fsTree[parent]) fsTree[parent] = [];
      fsTree[parent].push({
        name,
        path,
        kind: "dir",
        size: 4096,
        mode: "drwxr-xr-x",
        owner: "deploy",
        group: "deploy",
        mtime: Date.now(),
        symlinkTarget: null,
      });
      fsTree[path] = [];
      return null;
    }

    case "fs_rename": {
      const from = absPath(a.from);
      const to = absPath(a.to);
      const parent = from.slice(0, from.lastIndexOf("/")) || "/";
      const entry = fsTree[parent]?.find((e) => e.path === from);
      if (entry) {
        entry.path = to;
        entry.name = to.slice(to.lastIndexOf("/") + 1);
      }
      return null;
    }

    case "fs_delete": {
      const path = absPath(a.path);
      const parent = path.slice(0, path.lastIndexOf("/")) || "/";
      const list = fsTree[parent];
      if (list) {
        const i = list.findIndex((e) => e.path === path);
        if (i >= 0) list.splice(i, 1);
      }
      delete fsTree[path];
      delete fsFileContent[path];
      return null;
    }

    case "fs_chmod":
      return null;

    case "fs_checksum":
      return "d41d8cd98f00b204e9800998ecf8427e  (演示模式：固定示例值)";

    case "fs_upload":
    case "fs_download": {
      const taskId = uid("task");
      const total = 4_812_640;
      let done = 0;
      const timer = window.setInterval(() => {
        done = Math.min(total, done + total / 8);
        emit("fs://progress", { taskId, transferred: done, total, done: done >= total });
        if (done >= total) window.clearInterval(timer);
      }, 180);
      await new Promise((r) => window.setTimeout(r, 1500));
      window.clearInterval(timer);
      emit("fs://progress", { taskId, transferred: total, total, done: true });
      return total;
    }

    case "fs_pack_download": {
      // 和上一条共用一套假进度：演示模式里进度条也该动起来
      const taskId = uid("task");
      const total = 2_408_192;
      let done = 0;
      const timer = window.setInterval(() => {
        done = Math.min(total, done + total / 8);
        emit("fs://progress", { taskId, transferred: done, total, done: done >= total });
        if (done >= total) window.clearInterval(timer);
      }, 180);
      await new Promise((r) => window.setTimeout(r, 1200));
      window.clearInterval(timer);
      emit("fs://progress", { taskId, transferred: total, total, done: true });
      return total;
    }

    case "fs_extract": {
      // 与内核约定一致：返回"去掉压缩后缀的同名目录"
      const p = absPath(a.path);
      return p.replace(/\.(tar\.gz|tgz|tar\.bz2|tbz2|tbz|tar\.xz|txz|tar|zip)$/i, "");
    }

    /* ─────────────── mount ─────────────── */
    case "mount_list":
      return mounts.map((m) => ({ ...m }));

    case "mount_create": {
      const m = {
        id: uid("m"),
        localPoint: str(a.localPoint, "Z:"),
        remote: str(a.remotePath, "\\\\host\\share"),
        sessionId: str(a.sessionId),
        createdAt: Date.now(),
      };
      mounts.push(m);
      return { ...m };
    }

    case "mount_remove": {
      const i = mounts.findIndex((m) => m.localPoint === str(a.localPoint));
      if (i >= 0) mounts.splice(i, 1);
      return null;
    }

    /* ─────────────── docker ─────────────── */
    case "docker_ps":
      return containers.map((c) => ({ ...c }));

    case "docker_overview":
      return {
        containers: containers.map((c) => ({ ...c })),
        hostStats: { cpuPercent: 18.3, memUsedMb: 7468, memTotalMb: 7872, diskPercent: 77 },
      };

    case "docker_images":
      return images.map((i) => ({ ...i }));

    case "docker_stats": {
      const lines = containers
        .filter((c) => c.state === "running")
        .map((c) => {
          const s = containerStats[c.name] ?? { cpu: "0.1%", mem: "12MB" };
          return `${c.id.slice(0, 12)}|${c.name}|${s.cpu}|${s.mem}`;
        })
        .join("\n");
      return lines;
    }

    case "docker_action": {
      const c = containers.find((x) => x.id === str(a.containerId));
      const action = str(a.action);
      if (!c) return null;
      if (action === "remove") {
        const i = containers.findIndex((x) => x.id === c.id);
        if (i >= 0) containers.splice(i, 1);
        return null;
      }
      if (action === "stop") {
        c.state = "exited";
        c.status = "Exited (0) Less than a second ago";
      } else if (action === "restart" || action === "start") {
        c.state = "running";
        c.status = action === "restart" ? "Up Less than a second" : "Up Less than a second";
      } else if (action === "pause") {
        c.state = "paused";
        c.status = "Up 3 hours (Paused)";
      }
      return null;
    }

    case "docker_image_remove": {
      const i = images.findIndex((x) => x.id === str(a.image) || `${x.repository}:${x.tag}` === str(a.image));
      if (i >= 0) images.splice(i, 1);
      return null;
    }

    case "docker_image_pull": {
      const image = str(a.image, "nginx:alpine");
      emit("docker://stats", { pulling: image });
      await new Promise((r) => window.setTimeout(r, 2200));
      const [repo, tag] = image.includes(":") ? image.split(":") : [image, "latest"];
      images.unshift({
        id: `sha256:${Math.random().toString(16).slice(2, 10)}`,
        repository: repo,
        tag: tag ?? "latest",
        size: "48.9MB",
        createdSince: "Less than a second ago",
      });
      return "pulled";
    }

    case "docker_inspect":
      return {
        Id: `${str(a.containerId)}${"0".repeat(40)}`,
        Name: `/${containers.find((c) => c.id === str(a.containerId))?.name ?? "container"}`,
        State: { Status: "running", Running: true, Pid: 1183, RestartCount: 0 },
        Config: { Image: containers.find((c) => c.id === str(a.containerId))?.image ?? "" },
        HostConfig: { Memory: 2147483648, MemorySwap: 2147483648 },
      };

    case "docker_container_list_dir": {
      const path = str(a.path, "/");
      const fake: Record<string, string[]> = {
        "/": ["app/", "bin/", "etc/", "lib/", "tmp/", "usr/", "var/"],
        "/app": ["dist/", "node_modules/", "package.json", "server.js"],
      };
      return fake[path] ?? ["（演示模式：容器内只有 / 与 /app 两个目录有内容）"];
    }

    case "docker_logs_attach": {
      const tabId = newTabId("log");
      const name = containers.find((c) => c.id === str(a.containerId))?.name ?? "api-server";
      const initial = containerLogLines(name).slice(-num(a.tail, 20));
      later(60, () => {
        pushText(a.channel, `\x1b[2m# 跟随 ${name} 的日志（演示模式，每 4 秒补一行）—— Esc / 返回可停止\x1b[0m\r\n`);
        initial.forEach((l) => pushText(a.channel, `${l}\r\n`));
      });
      const stop = later(0, () => undefined);
      stop();
      let n = 0;
      const timer = window.setInterval(() => {
        n += 1;
        pushText(a.channel, `${new Date().toISOString().replace("T", " ").slice(0, 19)} INFO  heartbeat #${n} db=ok redis=ready\r\n`);
      }, 4000);
      logTimers.set(tabId, () => window.clearInterval(timer));
      return tabId;
    }

    case "docker_exec_attach": {
      const tabId = newTabId("exec");
      const name = containers.find((c) => c.id === str(a.containerId))?.name ?? "api-server";
      const shell = new DemoShell((text) => pushText(a.channel, text.replace(/\n/g, "\r\n")));
      shells.set(tabId, shell);
      later(90, () => {
        pushText(a.channel, `\r\n\x1b[2m[演示模式] 已进入容器 ${name} 的 sh\x1b[0m\r\n\r\n`);
        shell.start();
      });
      return tabId;
    }

    /* ─────────────── db ─────────────── */
    case "db_connect":
      return { connId: uid("conn") };

    case "db_disconnect":
      return null;

    case "db_schemas":
      return [...mysqlSchemas];

    case "db_tables":
      return [...(mysqlTables[str(a.schema, "shop")] ?? mysqlTables.shop)];

    case "db_columns": {
      const table = str(a.table, "orders");
      return {
        columns: mysqlColumns[table] ?? mysqlColumns.orders,
        indexes: mysqlIndexes[table] ?? mysqlIndexes.orders,
      };
    }

    case "db_query":
      return fakeQuery(str(a.sql));

    case "redis_scan": {
      const cursor = num(a.cursor, 0);
      const count = num(a.count, 200);
      const pattern = str(a.pattern, "*");
      const re = new RegExp(`^${pattern.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*")}$`);
      const matched = redisKeys.filter((k) => re.test(k));
      if (cursor === 0) return [0, matched.slice(0, count)];
      return [0, []];
    }

    case "redis_inspect": {
      const key = str(a.key);
      const v = redisValues[key];
      if (v) return { key, keyType: v.keyType, ttl: v.ttl, value: v.value };
      return { key, keyType: "string", ttl: -1, value: "(演示模式：该键没有内置值)" };
    }

    case "redis_command": {
      const args = (a.args as string[]) ?? [];
      const cmd = (args[0] ?? "").toUpperCase();
      if (cmd === "PING") return "PONG";
      if (cmd === "INFO") {
        return [
          "# Server",
          "redis_version:7.2.4",
          "uptime_in_days:87",
          "# Memory",
          "used_memory_human:48.20M",
          "maxmemory_human:512.00M",
          "maxmemory_policy:allkeys-lru",
          "# Stats",
          "keyspace_hits:18422014",
          "keyspace_misses:481220",
        ].join("\n");
      }
      if (cmd === "DBSIZE") return "(integer) 10";
      if (cmd === "KEYS") return redisKeys.join("\n");
      if (cmd === "GET") return redisValues[str(args[1])] ? JSON.stringify(redisValues[str(args[1])].value) : "(nil)";
      return `(演示模式) 已执行 ${args.join(" ")}`;
    }

    case "redis_set_ttl":
      return null;

    /* ─────────────── ai ─────────────── */
    case "ai_chat": {
      jobSeq += 1;
      const jobId = `job-${jobSeq}`;
      // 镜像真机：首轮没带 id → 新建会话；**并把 id 回传**给前端，后续追问带着它回来
      // 才落在同一个会话里。真机曾经漏了这回传，于是每追问一次就多出一个历史项。
      const requested = str(a.conversationId) || liveConvId;
      if (requested) {
        liveConvId = requested;
      } else {
        liveConvId = uid("conv");
        conversations.push({
          id: liveConvId,
          title: str(a.message).split("\n")[0].trim().slice(0, 24) || "新对话",
          scope: {},
          createdAt: Date.now(),
          updatedAt: Date.now(),
        });
      }
      aiChannels.set(jobId, a.channel);
      streamAnswer(a.channel, jobId, str(a.message), Boolean(a.planMode));
      return { jobId, conversationId: liveConvId };
    }

    case "ai_cancel": {
      const jobId = str(a.jobId);
      cancelledAiJobs.add(jobId);
      pendingAi.delete(jobId);
      // 真机取消后会推一条 `error("已取消")`，界面据此复位。演示模式也推一份 ——
      // 只把 jobId 记下来的话，停止按钮按下去是静默失效，看不出到底生效没有。
      pushEvent(aiChannels.get(jobId), {
        type: "error",
        message: "已取消",
        retryable: false,
      });
      aiChannels.delete(jobId);
      return null;
    }

    case "ai_confirm": {
      const jobId = str(a.jobId);
      const fn = pendingAi.get(jobId);
      pendingAi.delete(jobId);
      fn?.(str(a.decision, "allow"));
      return null;
    }

    case "ai_models":
      return ["deepseek-chat", "deepseek-reasoner", "gpt-4o-mini", "qwen-plus"];

    /* ── 多模型档案（P0-3） ── */

    case "ai_model_profiles":
      return {
        profiles: modelState.profiles.map((p) => ({ ...p })),
        activeId: modelState.activeId,
      };

    case "ai_model_save": {
      const p = rawArgs?.profile as (typeof modelState.profiles)[number] | undefined;
      if (!p || typeof p !== "object") return null;
      const saved = { ...p, id: p.id || `m-${uid("m")}` };
      const idx = modelState.profiles.findIndex((x) => x.id === saved.id);
      if (idx >= 0) modelState.profiles[idx] = saved;
      else modelState.profiles.push(saved);
      // 第一份档案自动成为当前 —— 否则用户存完发现"用不了"，会以为坏了
      if (modelState.profiles.length === 1) modelState.activeId = saved.id;
      return saved;
    }

    case "ai_model_delete": {
      const id = str(a.id);
      modelState.profiles = modelState.profiles.filter((p) => p.id !== id);
      if (modelState.activeId === id) modelState.activeId = modelState.profiles[0]?.id ?? "";
      return null;
    }

    case "ai_model_activate":
      modelState.activeId = str(a.id);
      return null;

    case "ai_model_refresh":
      await new Promise((r) => window.setTimeout(r, 600));
      return ["deepseek-chat", "deepseek-reasoner", "deepseek-coder"];

    case "ai_model_preset": {
      const name = str(a.preset);
      const blank = {
        id: "",
        name: name || "新模型",
        baseUrl: "https://api.example.com/v1",
        apiKey: "",
        model: "",
        temperature: 0.3,
        contextWindow: 32768,
        proxy: null,
        stream: true,
      };
      if (name === "deepseek") {
        return { ...blank, name: "DeepSeek", baseUrl: "https://api.deepseek.com/v1", model: "deepseek-chat", contextWindow: 64000 };
      }
      if (name === "openai") {
        return { ...blank, name: "OpenAI", baseUrl: "https://api.openai.com/v1", model: "gpt-4o-mini", contextWindow: 128000 };
      }
      if (name === "ollama") {
        return { ...blank, name: "Ollama", baseUrl: "http://127.0.0.1:11434/v1", model: "qwen2.5:7b", contextWindow: 32000 };
      }
      return blank;
    }

    case "ai_presets":
      return [...providerPresets];

    case "ai_get_provider":
      return { ...providerConfig, apiKey: providerConfig.apiKey ? MASK : "" };

    case "ai_get_permission":
      return { ...permissionConfig, dangerRules: [...permissionConfig.dangerRules] };

    case "ai_set_permission": {
      const c = (rawArgs?.config ?? {}) as Partial<typeof permissionConfig>;
      if (c && typeof c === "object") Object.assign(permissionConfig, c);
      return null;
    }

    case "ai_set_provider": {
      const c = params(rawArgs) as unknown as typeof providerConfig;
      const cfg = (rawArgs?.config ?? c) as typeof providerConfig;
      if (cfg && typeof cfg === "object") Object.assign(providerConfig, cfg);
      return null;
    }

    case "ai_test_provider":
      await new Promise((r) => window.setTimeout(r, 900));
      return { modelsOk: true, chatOk: true };

    case "ai_conversation_list":
      return conversations.map((c) => ({ ...c }));

    case "ai_conversation_create": {
      const c = {
        id: uid("conv"),
        title: str(a.title, "新对话"),
        scope: {},
        createdAt: Date.now(),
        updatedAt: Date.now(),
      };
      conversations.push(c);
      return { ...c };
    }

    case "ai_conversation_delete": {
      const i = conversations.findIndex((c) => c.id === str(a.id));
      if (i >= 0) conversations.splice(i, 1);
      return null;
    }

    case "ai_messages": {
      // 曾经这里无条件返回 []，于是演示模式下"点进历史会话"永远一片空白 ——
      // 而真机那条路坏在别处（返回 Row 而非 DTO），两条路各坏各的，谁都盖不住谁。
      const id = str(a.conversationId);
      return (conversationMessages[id] ?? []).map((m) => ({ ...m, conversationId: id }));
    }

    case "ai_takeover_enter":
    case "ai_takeover_exit":
      return null;

    case "ai_takeover_run": {
      jobSeq += 1;
      const jobId = `job-${jobSeq}`;
      const instruction = str(a.instruction, "执行任务");
      const screens = [
        "deploy@web-01:~$ sudo apt-get install -y nginx\nWaiting for cache lock: Could not get lock /var/lib/dpkg/lock-frontend",
        "deploy@web-01:~$ sudo lsof /var/lib/dpkg/lock-frontend\nCOMMAND   PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME\nunattended  912 root    3uW  REG  253,1        0  421 /var/lib/dpkg/lock-frontend",
        "deploy@web-01:~$ sudo systemctl stop unattended-upgrades\ndeploy@web-01:~$ sudo apt-get install -y nginx\nReading package lists... Done\nSetting up nginx (1.24.0-2ubuntu7.2) ...",
        "deploy@web-01:~$ systemctl is-active nginx\nactive\ndeploy@web-01:~$ curl -sI http://127.0.0.1 | head -1\nHTTP/1.1 200 OK",
      ];
      screens.forEach((text, i) => {
        later(700 + i * 1500, () => pushEvent(a.channel, { type: "screen", text }));
      });
      later(700 + screens.length * 1500, () => {
        pushEvent(a.channel, { type: "delta", text: "已完成「" });
        pushEvent(a.channel, { type: "delta", text: instruction });
        pushEvent(a.channel, { type: "delta", text: "」。" });
      });
      later(700 + screens.length * 1500 + 600, () =>
        pushEvent(a.channel, {
          type: "done",
          answer:
            "先被 unattended-upgrades 占着 dpkg 锁，停掉它之后 nginx 装上了；`systemctl is-active` 返回 active，本机 80 端口返回 200。全程 4 步都在你面前的终端里，可逐屏回放。",
        }),
      );
      return jobId;
    }

    /* ─────────────── vault ─────────────── */
    case "vault_status":
      return { ...vaultState };

    case "vault_init_master":
      Object.assign(vaultState, { initialized: true, mode: "master", unlocked: true });
      return null;

    case "vault_init_dpapi":
      Object.assign(vaultState, { initialized: true, mode: "dpapi", unlocked: true });
      return null;

    case "vault_unlock":
      vaultState.unlocked = true;
      return null;

    case "vault_lock":
      vaultState.unlocked = false;
      return null;

    case "vault_change_password":
      return null;

    case "vault_set_credential": {
      // 带 id = 原位更新（对齐真机"已绑私钥凭据就地改"的语义），不带 = 新建
      const src: "inline" | "file" = a.source === "file" ? "file" : "inline";
      const existing = a.id ? demoCredentials.find((c) => c.id === str(a.id)) : undefined;
      if (existing) {
        if (typeof a.name === "string") existing.name = a.name;
        if (typeof a.kind === "string") existing.kind = a.kind;
        if (typeof a.secret === "string") existing.secret = a.secret;
        if (existing.kind === "private_key") {
          existing.source = src;
          // 口令"提供即覆盖"，空串/缺省 = 不动（对齐真机 credential_update）
          if (typeof a.passphrase === "string" && a.passphrase) existing.passphrase = a.passphrase;
        }
        existing.updatedAt = Date.now();
        return { id: existing.id };
      }
      const c: DemoCredential = {
        id: uid("cred"),
        name: str(a.name, "新凭据"),
        kind: str(a.kind, "password"),
        secret: str(a.secret),
        createdAt: Date.now(),
        updatedAt: Date.now(),
      };
      if (c.kind === "private_key") {
        c.source = src;
        if (typeof a.passphrase === "string" && a.passphrase) c.passphrase = a.passphrase;
      }
      demoCredentials.push(c);
      return { id: c.id };
    }

    case "credential_update": {
      const c = demoCredentials.find((x) => x.id === str(a.id));
      if (c) {
        if (typeof a.name === "string" && a.name.trim()) c.name = a.name.trim();
        if (typeof a.secret === "string" && a.secret) {
          c.secret = a.secret;
          // 改了值就跟着改来源（真机在未提供 source 时沿用原来源，这里由调用方保证传对）
          if (c.kind === "private_key" && typeof a.source === "string") {
            c.source = a.source === "file" ? "file" : "inline";
          }
        }
        if (c.kind === "private_key" && typeof a.passphrase === "string") {
          // 空串 = 清除口令
          c.passphrase = a.passphrase.trim() ? a.passphrase : undefined;
        }
        c.updatedAt = Date.now();
      }
      return null;
    }

    case "vault_list_credentials":
      return demoCredentials.map((c) => {
        const isKey = c.kind === "private_key";
        const isRef = isKey && c.source === "file";
        // 来源藏在密文里：锁定态一律给空（对齐真机，免得界面显示"没有口令"这种假信息）
        const readable = isKey && vaultState.unlocked;
        return {
          id: c.id,
          name: c.name,
          kind: c.kind,
          createdAt: c.createdAt,
          updatedAt: c.updatedAt,
          usedBy: assets
            .filter((x) => x.credId === c.id && x.deletedAt === null)
            .map((x) => ({ id: x.id, name: x.name, kind: x.kind })),
          source: readable ? (c.source ?? "inline") : null,
          refPath: readable && isRef ? c.secret : null,
          hasPassphrase: readable ? !!c.passphrase : false,
        };
      });

    case "vault_delete_credential": {
      const i = demoCredentials.findIndex((c) => c.id === str(a.id));
      if (i >= 0) demoCredentials.splice(i, 1);
      // 对齐真机 FK 的 ON DELETE SET NULL：删凭据后资产的引用清空、资产保留
      for (const x of assets) if (x.credId === str(a.id)) x.credId = null;
      return null;
    }

    case "vault_reveal_credential": {
      if (!vaultState.unlocked) throw new Error("凭据库已锁定，请先解锁");
      const c = demoCredentials.find((x) => x.id === str(a.id));
      if (!c) throw new Error("找不到这条凭据");
      if (c.kind === "private_key") {
        const isRef = c.source === "file";
        // 引用型没有正文可给（库里本来就没有），路径单独给
        return {
          kind: c.kind,
          value: isRef ? "" : c.secret,
          source: c.source ?? "inline",
          refPath: isRef ? c.secret : null,
          passphrase: c.passphrase ?? null,
        };
      }
      return { kind: c.kind, value: c.secret || MASK, source: null, refPath: null, passphrase: null };
    }

    /* ─────────────── port forward ─────────────── */
    case "forward_list":
      return forwards.map((f) => ({ ...f }));

    case "forward_create": {
      const f = {
        id: uid("f"),
        sessionId: str(a.sessionId),
        listenPort: num(a.listenPort, 13306),
        targetHost: str(a.targetHost, "127.0.0.1"),
        targetPort: num(a.targetPort, 3306),
        kind: "local",
        createdAt: Date.now(),
      };
      forwards.push(f);
      return { ...f };
    }

    case "forward_create_socks": {
      // 演示模式不真起监听，只把这条记录塞进列表 —— 面板行为与真机一致
      const f = {
        id: uid("f"),
        sessionId: str(a.sessionId),
        listenPort: num(a.listenPort, 1080),
        targetHost: null,
        targetPort: null,
        kind: "socks",
        createdAt: Date.now(),
      };
      forwards.push(f);
      return { ...f };
    }

    case "forward_remove": {
      const i = forwards.findIndex((f) => f.id === str(a.id));
      if (i >= 0) forwards.splice(i, 1);
      return null;
    }

    default:
      // 演示模式下未覆盖的命令一律静默成功，避免面板卡在 loading
      return null;
  }
}
