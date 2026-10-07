
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

const sessionEventVersions = new Map<string, number>();

function emitSessionStatus(sessionId: string, status: string, error: string | null) {
  const version = (sessionEventVersions.get(sessionId) ?? 0) + 1;
  sessionEventVersions.set(sessionId, version);
  emit("session://status", { sessionId, status, error, version });
}

const shells = new Map<string, DemoShell>();
const logTimers = new Map<string, () => void>();
const pendingAi = new Map<string, (decision: string) => void>();
const cancelledAiJobs = new Set<string>();
const aiChannels = new Map<string, unknown>();
const activeAiJobs = new Map<string, string>();
let jobSeq = 0;

function cancelAiJob(jobId: string) {
  cancelledAiJobs.add(jobId);
  pendingAi.delete(jobId);
  activeAiJobs.delete(jobId);
  pushEvent(aiChannels.get(jobId), {
    type: "error",
    message: "已取消",
    retryable: false,
  });
  aiChannels.delete(jobId);
}

const demoRuns = [
  {
    id: "run-1",
    conversationId: "conv-1",
    status: "completed",
    attempt: 1,
    seq: 12,
    planMode: false,
    source: "chat",
    profileId: "m-deepseek",
    answer: "已定位 502 来自上游 api-server 的连接池耗尽，建议先扩容并加连接复用。",
    turns: 4,
    tokensIn: 3120,
    tokensOut: 486,
    cacheCreationTokens: 0,
    latencyMs: 5230,
    retries: 0,
    failures: 0,
    createdAt: Date.now() - 21 * 60_000,
    updatedAt: Date.now() - 20 * 60_000,
    finishedAt: Date.now() - 20 * 60_000,
  },
  {
    id: "run-2",
    conversationId: "conv-1",
    status: "superseded",
    attempt: 2,
    seq: 13,
    planMode: false,
    source: "chat",
    profileId: "m-deepseek",
    answer: "",
    turns: 1,
    tokensIn: 980,
    tokensOut: 0,
    cacheCreationTokens: 0,
    latencyMs: 1200,
    retries: 0,
    failures: 0,
    createdAt: Date.now() - 19 * 60_000,
    updatedAt: Date.now() - 19 * 60_000,
    finishedAt: Date.now() - 19 * 60_000,
  },
];

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

const layoutState: { revision: number; updatedAt: number; data: unknown | null } = {
  revision: 0,
  updatedAt: 0,
  data: null,
};

interface DemoMemoryEntry {
  id: string;
  tenant: string;
  subject: string;
  topic: string;
  content: string;
  version: number;
  redacted: boolean;
  createdAt: number;
  updatedAt: number;
}

interface DemoMemorySettings {
  injectionEnabled: boolean;
  toolsEnabled: boolean;
  version: number;
}

const demoMemoryEntries: DemoMemoryEntry[] = [
  {
    id: "mem-nginx-restart",
    tenant: "local",
    subject: "default",
    topic: "operations",
    content: "web-01 的 nginx 每天 02:00 定时重启，重启后需要确认 80 端口返回 200。",
    version: 1,
    redacted: false,
    createdAt: Date.now() - 86_400_000,
    updatedAt: Date.now() - 86_400_000,
  },
];
const demoMemorySettings = new Map<string, DemoMemorySettings>();

const demoSecretRules = [
  /-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)/gi,
  /\bBearer\s+[A-Za-z0-9._~+/=-]+/gi,
  /\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\b/g,
  /([a-z][a-z0-9+.-]*:\/\/[^\s/:@]+:)[^\s/@]+@/gi,
  /(?:[a-z0-9]+_)*(?:api[_-]?key|access[_-]?token|refresh[_-]?token|session[_-]?token|client[_-]?secret|password|passwd|secret|token|authorization)(?:_(?:access|key|keys))*["']?\s*[:=]\s*("[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)/gi,
];

function demoMemoryRedact(content: string): { content: string; changed: boolean } {
  let redacted = content;
  for (const rule of demoSecretRules) {
    redacted = redacted.replace(rule, "[REDACTED]");
  }
  return { content: redacted, changed: redacted !== content };
}

function demoMemorySanitize(content: string, policy: string): { content: string; redacted: boolean } {
  if (policy !== "reject" && policy !== "redact") {
    throwAppError("bad_param", "secrets 必须是 reject 或 redact");
  }
  const result = demoMemoryRedact(content);
  if (result.changed && policy !== "redact") {
    throwAppError("bad_param", "semantic memory contains a possible secret");
  }
  return { content: result.content, redacted: result.changed };
}

function demoMemoryScopeKey(tenant: string, subject: string): string {
  return `${tenant}\n${subject}`;
}

function demoMemoryRequireScope(scope: { tenant?: string; subject?: string }): { tenant: string; subject: string } {
  const tenant = str(scope.tenant).trim();
  const subject = str(scope.subject).trim();
  if (!tenant || !subject) throwAppError("forbidden", "semantic memory access denied");
  return { tenant, subject };
}

function demoMemoryFind(scope: { tenant: string; subject: string }, id: string): DemoMemoryEntry {
  const entry = demoMemoryEntries.find((e) => e.id === id);
  if (!entry) throwAppError("not_found", `semantic memory not found: ${id}`);
  if (entry.tenant !== scope.tenant || entry.subject !== scope.subject) {
    throwAppError("forbidden", "semantic memory access denied");
  }
  return entry;
}

function demoMemoryCAS(entry: { id: string; version: number }, expectedVersion: number) {
  if (entry.version !== expectedVersion) {
    throwAppError(
      "bad_param",
      `semantic memory version conflict: ${entry.id} (expected ${expectedVersion}, actual ${entry.version})`,
      { id: entry.id, expected: expectedVersion, actual: entry.version },
    );
  }
}

function throwAppError(code: string, message: string, detail?: Record<string, unknown>): never {
  throw { code, message, ...(detail === undefined ? {} : { detail }) };
}

function isLoopbackHost(host: string): boolean {
  const normalized = host.trim().toLowerCase();
  return (
    normalized === "localhost" ||
    normalized.startsWith("127.") ||
    normalized === "::1" ||
    normalized === "0:0:0:0:0:0:0:1"
  );
}

const DEMO_NGINX_PATH = "/etc/nginx/nginx.conf";
const DEMO_TAKEOVER_TOKEN = "demo-takeover";
const DEMO_NGINX_BEFORE = "worker_processes 1;\nkeepalive_timeout 65;\nserver_tokens on;";
const DEMO_NGINX_AFTER =
  "worker_processes auto;\nkeepalive_timeout 65;\nserver_tokens off;\nclient_max_body_size 64m;";
let liveConvId: string | undefined;

function newTabId(prefix = "t"): string {
  return `${prefix}-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
}

const MASK = "••••••••••••";

interface DemoCredential {
  id: string;
  name: string;
  kind: string;
  secret: string;
  source?: "inline" | "file";
  passphrase?: string;
  createdAt: number;
  updatedAt: number;
}

const demoCredentials: DemoCredential[] = [
  { id: "cred-web01", name: "web-01", kind: "password", secret: "Xk9#web01$pw", createdAt: Date.now() - 20 * 86_400_000, updatedAt: Date.now() - 86_400_000 },
  { id: "cred-dbprod", name: "db-prod", kind: "password", secret: "Prod#db2026!", createdAt: Date.now() - 25 * 86_400_000, updatedAt: Date.now() - 3 * 86_400_000 },
  { id: "cred-nat", name: "nat-01", kind: "password", secret: "Nat0ld!2023", createdAt: Date.now() - 60 * 86_400_000, updatedAt: Date.now() - 30 * 86_400_000 },
  { id: "cred-old", name: "旧机房-口令", kind: "passphrase", secret: "old-machine-room", createdAt: Date.now() - 200 * 86_400_000, updatedAt: Date.now() - 90 * 86_400_000 },
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

const vaultState = {
  initialized: true,
  mode: "dpapi" as "dpapi" | "master",
  unlocked: true,
  autoLockMinutes: 30,
};

const demoFilesSettings = {
  publicBaseURL: "",
};

const demoInlinePem =
  "-----BEGIN OPENSSH PRIVATE KEY-----\n（演示模式：这是一段假私钥，仅用于展示流程）\n-----END OPENSSH PRIVATE KEY-----";

interface DemoSshImportHost {
  id: string;
  alias: string;
  hostname: string;
  port: number;
  username: string;
  identityFiles: string[];
  keyName: string;
  proxyJump: string;
  authMethod: string;
  source: string;
  action: string;
  warnings: string[];
}

interface DemoSshImportKey {
  id: string;
  aliases: string[];
  fingerprint: string;
  keyType: string;
  path: string;
  source: string;
  action: string;
  warnings: string[];
}

interface DemoSshImportPreview {
  source: string;
  path: string;
  hosts: DemoSshImportHost[];
  keys: DemoSshImportKey[];
  diagnostics: { code: string; source: string; message: string }[];
  truncated: boolean;
}

const demoSshImportPreviews: Record<string, DemoSshImportPreview> = {
  "ssh-config": {
    source: "ssh-config",
    path: "~/.ssh/config",
    hosts: [
      {
        id: "h0",
        alias: "bastion",
        hostname: "bastion.demo.internal",
        port: 22,
        username: "admin",
        identityFiles: [],
        keyName: "",
        proxyJump: "",
        authMethod: "agent",
        source: "ssh-config",
        action: "add",
        warnings: [],
      },
      {
        id: "h1",
        alias: "web-02",
        hostname: "web-02.demo.internal",
        port: 22,
        username: "deploy",
        identityFiles: ["/home/demo/.ssh/id_rsa_demo"],
        keyName: "",
        proxyJump: "bastion",
        authMethod: "key",
        source: "ssh-config",
        action: "add",
        warnings: [],
      },
      {
        id: "h2",
        alias: "web-01",
        hostname: "127.0.0.1",
        port: 22,
        username: "deploy",
        identityFiles: [],
        keyName: "",
        proxyJump: "",
        authMethod: "agent",
        source: "ssh-config",
        action: "skip-duplicate",
        warnings: ["别名 web-01 已存在且端点相同，将跳过"],
      },
      {
        id: "h3",
        alias: "nat-01",
        hostname: "nat-new.demo.internal",
        port: 22,
        username: "root",
        identityFiles: [],
        keyName: "",
        proxyJump: "",
        authMethod: "agent",
        source: "ssh-config",
        action: "conflict-alias",
        warnings: ["别名 nat-01 已被一台端点不同的资产使用"],
      },
    ],
    keys: [
      {
        id: "k0",
        aliases: ["id_rsa_demo"],
        fingerprint: "SHA256:demoRsaFingerprint0000000000000000000000000",
        keyType: "ssh-rsa",
        path: "/home/demo/.ssh/id_rsa_demo",
        source: "ssh-config",
        action: "add",
        warnings: [],
      },
      {
        id: "k1",
        aliases: ["id_ed25519"],
        fingerprint: "SHA256:demoOtherFingerprint000000000000000000000000",
        keyType: "ssh-ed25519",
        path: "/home/demo/.ssh/id_ed25519",
        source: "ssh-config",
        action: "conflict-alias",
        warnings: ["密钥名 id_ed25519 已被一个指纹不同的密钥使用"],
      },
    ],
    diagnostics: [
      {
        code: "host-pattern-skipped",
        source: "~/.ssh/config:12",
        message: "通配 Host 模式已跳过（不支持导入）",
      },
    ],
    truncated: false,
  },
  termius: {
    source: "termius",
    path: "~/Library/Application Support/Termius/IndexedDB/file__0.indexeddb.leveldb",
    hosts: [
      {
        id: "h0",
        alias: "termius-prod",
        hostname: "prod.demo.internal",
        port: 22,
        username: "ubuntu",
        identityFiles: [],
        keyName: "termius-key",
        proxyJump: "",
        authMethod: "key",
        source: "termius",
        action: "add",
        warnings: [],
      },
      {
        id: "h1",
        alias: "termius-staging",
        hostname: "staging.demo.internal",
        port: 22,
        username: "ubuntu",
        identityFiles: [],
        keyName: "",
        proxyJump: "",
        authMethod: "password",
        source: "termius",
        action: "add",
        warnings: ["密码不会导入，导入后请手动绑定凭据"],
      },
      {
        id: "h2",
        alias: "termius-prod",
        hostname: "prod-old.demo.internal",
        port: 22,
        username: "ubuntu",
        identityFiles: [],
        keyName: "",
        proxyJump: "",
        authMethod: "agent",
        source: "termius",
        action: "conflict-alias",
        warnings: ["别名 termius-prod 与本次导入内的另一台主机冲突"],
      },
    ],
    keys: [
      {
        id: "k0",
        aliases: ["termius-key"],
        fingerprint: "SHA256:demoTermiusFingerprint00000000000000000000",
        keyType: "ssh-ed25519",
        path: "",
        source: "termius",
        action: "add",
        warnings: [],
      },
    ],
    diagnostics: [],
    truncated: false,
  },
};

interface DemoSnippet {
  id: string;
  groupId: string | null;
  name: string;
  body: string;
  sort: number;
  createdAt: number;
  updatedAt: number;
}

const demoSnippets: DemoSnippet[] = [
  { id: "sn1", groupId: null, name: "看容器状态", body: "docker ps --format '{{.Names}}\\t{{.Status}}'", sort: 1, createdAt: Date.now() - 9 * 86_400_000, updatedAt: Date.now() - 86_400_000 },
  { id: "sn2", groupId: null, name: "磁盘水位", body: "df -h && du -sh /data/*", sort: 2, createdAt: Date.now() - 8 * 86_400_000, updatedAt: Date.now() - 2 * 86_400_000 },
  { id: "sn3", groupId: null, name: "nginx 重载", body: "sudo nginx -t && sudo nginx -s reload", sort: 3, createdAt: Date.now() - 7 * 86_400_000, updatedAt: Date.now() - 3 * 86_400_000 },
];

const bundleFiles = new Map<string, { content: string; encrypted: boolean; password: string }>();

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

const DEMO_HOME = "/home/deploy";

function absPath(v: unknown): string {
  const raw = str(v, DEMO_HOME);
  if (raw === "~") return DEMO_HOME;
  if (raw.startsWith("~/")) return DEMO_HOME + raw.slice(1);
  return raw.replace(/\/+$/, "") || "/";
}

const BASE64_CHUNK_BYTES = 32_766;

function bytesToBase64(bytes: Uint8Array): string {
  let b64 = "";
  for (let i = 0; i < bytes.length; i += BASE64_CHUNK_BYTES) {
    b64 += btoa(String.fromCharCode(...bytes.subarray(i, i + BASE64_CHUNK_BYTES)));
  }
  return b64;
}

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
  const channel = {
    onmessage: (evt: Record<string, unknown>) => {
      if (cancelledAiJobs.has(jobId)) return;
      pushEvent(rawChannel, evt);
    },
    toJSON: () => null,
  };
  const { answer, commands } = answerFor(question);
  const yesNo = question.includes("重启") || question.includes("restart") || question.includes("删除");

  const cmdsBlock = commands.length
    ? "```bash\n" + commands.map((c) => `$ ${c}`).join("\n") + "\n```\n"
    : "";
  const fullAnswer = `${answer}\n\n${cmdsBlock}—— 以上命令都已在你面前的终端里跑过，可回放。`;

  let delay = 430;

  later(140, () =>
    pushEvent(channel, {
      type: "usage",
      promptTokens: 8420,
      completionTokens: 386,
      cachedTokens: 6016,
      contextWindow: 64000,
    }),
  );

  const thinking = ["先看容器状态", "，确认是不是进程", "没了；", "再查内存", "，", "OOM 的可能性", "最大。"];
  thinking.forEach((t, i) =>
    later(70 + i * 45, () => pushEvent(channel, { type: "reasoning", text: t })),
  );

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
    later(560, () => {
      activeAiJobs.delete(jobId);
      pushEvent(channel, { type: "done", answer: plan });
    });
    return;
  }

  if (/写|新建|保存|创建|改一下|加一行/.test(question)) {
    const callId = `call-${uid("c")}`;
    const creating = /新建|创建/.test(question);
    const path = creating ? "/etc/nginx/conf.d/upload.conf" : DEMO_NGINX_PATH;
    const before = creating ? "" : DEMO_NGINX_BEFORE;
    const after = creating
      ? "client_max_body_size 64m;\nproxy_read_timeout 120s;\n"
      : DEMO_NGINX_AFTER;
    const verb = creating ? "新建" : "修改";

    later(400, () => pushEvent(channel, { type: "status", phase: "thinking", turn: 1 }));
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
        rendered: `write_file {"path":"${path}","content":"…"}\n这是本会话第 1 次请求写权限。`,
        reason: "这是本会话第 1 次请求写权限。",
        preview: { path, kind: creating ? "create" : "modify", before, after },
        confirmationNonce: `nonce-${uid("n")}`,
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
        preview: null,
        confirmationNonce: `nonce-${uid("n")}`,
      });
      pendingAi.set(jobId, (decision) => {
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
    later(delay - 160, () =>
      pushEvent(channel, { type: "status", phase: "thinking", turn: 2 }),
    );
    later(200 + commands.length * 260, () =>
      pushEvent(channel, {
        type: "fileChange",
        id: `call-${uid("c")}`,
        path: DEMO_NGINX_PATH,
        before: DEMO_NGINX_BEFORE,
        after: DEMO_NGINX_AFTER,
      }),
    );
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
      activeAiJobs.delete(jobId);
      emitSessionStatus("s-web01", "connected", null);
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
    return containerLogLines("api-server").join("\n");
  }
  if (cmd.startsWith("docker exec redis")) {
    return "used_memory:50544640\nused_memory_human:48.20M\nmaxmemory:536870912\nmaxmemory_policy:allkeys-lru";
  }
  if (cmd.startsWith("nginx -t")) {
    return "nginx: configuration file /etc/nginx/nginx.conf test is successful";
  }
  if (cmd.startsWith("cat /etc/nginx")) {
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

function demoFsEntryExists(path: string): boolean {
  const parent = path.slice(0, path.lastIndexOf("/")) || "/";
  return fsTree[parent]?.some((e) => e.path === path) === true;
}

function failTransfer(message: string): never {
  emit("fs://progress", { taskId: uid("task"), transferred: 0, total: 0, done: true, error: message });
  throw new Error(message);
}

async function simulateTransfer(
  total: number,
  durationMs: number,
  failMessage: string | null,
): Promise<number> {
  const taskId = uid("task");
  const failAt = failMessage === null ? total : Math.floor(total / 2);
  let done = 0;
  const timer = window.setInterval(() => {
    done = Math.min(failAt, done + total / 8);
    emit("fs://progress", { taskId, transferred: done, total, done: false });
  }, 180);
  await new Promise((r) => window.setTimeout(r, durationMs));
  window.clearInterval(timer);
  if (failMessage !== null) {
    emit("fs://progress", { taskId, transferred: done, total, done: true, error: failMessage });
    throw new Error(failMessage);
  }
  emit("fs://progress", { taskId, transferred: total, total, done: true });
  return total;
}

export async function mockInvoke(cmd: string, rawArgs?: Record<string, unknown>): Promise<unknown> {
  const a = params(rawArgs);
  if (rawArgs && typeof rawArgs === "object") {
    const top = rawArgs as Record<string, unknown>;
    if ("channel" in top && !("channel" in a)) a.channel = top.channel;
  }
  await new Promise((r) => window.setTimeout(r, 40 + Math.random() * 60));

  switch (cmd) {
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
        emitSessionStatus(s.id, "connected", null);
      });
      return { ...s };
    }

    case "session_disconnect": {
      const i = sessions.findIndex((s) => s.id === str(a.sessionId));
      if (i >= 0) {
        const s = sessions[i];
        if (s.kind === "ssh" || s.kind === "docker" || s.kind === "winrm") {
          s.status = "disconnected";
          s.tabs = [];
        } else {
          sessions.splice(i, 1);
        }
        emitSessionStatus(s.id, "disconnected", null);
      }
      return null;
    }

    case "session_probe":
      return { open: true };

    case "session_reconnect": {
      const s = sessions.find((x) => x.id === str(a.sessionId));
      if (!s || !s.assetId || s.kind === "local") return false;
      s.status = "connecting";
      emitSessionStatus(s.id, "connecting", null);
      later(600, () => {
        s.status = "connected";
        emitSessionStatus(s.id, "connected", null);
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
      void line;
      return null;
    }

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
      const tabId = str(a.tabId);
      const lt = liveTabs.get(tabId);
      if (!lt) throwAppError("not_found", "终端标签不存在（可能进程已结束）");
      const client = str(a.clientId) || "desktop";
      lt.subscribers += 1;
      if (!lt.controller) lt.controller = client;
      lt.lastOutputAt = Date.now();
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
      return 4096;

    case "layout_get":
      return {
        revision: layoutState.revision,
        updatedAt: layoutState.updatedAt,
        data: layoutState.data,
      };

    case "layout_put": {
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
      const rev = layoutState.revision;
      later(0, () => emit("layout://changed", { revision: rev }));
      return { saved: true, revision: rev, conflict: false };
    }

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
      return demoSnippets.map((s) => ({ ...s }));

    case "snippet_create": {
      const snippet: DemoSnippet = {
        id: uid("sn"),
        groupId: str(a.groupId).trim() || null,
        name: str(a.name, "未命名片段"),
        body: str(a.body),
        sort: num(a.sort, 0),
        createdAt: Date.now(),
        updatedAt: Date.now(),
      };
      demoSnippets.push(snippet);
      return { id: snippet.id };
    }

    case "snippet_update": {
      const snippet = demoSnippets.find((s) => s.id === str(a.id));
      if (!snippet) throwAppError("not_found", "片段不存在");
      snippet.name = str(a.name, snippet.name);
      snippet.body = str(a.body, snippet.body);
      if (typeof a.groupId === "string" || a.groupId === null) {
        snippet.groupId = str(a.groupId).trim() || null;
      }
      if (typeof a.sort === "number") snippet.sort = a.sort;
      snippet.updatedAt = Date.now();
      return null;
    }

    case "snippet_delete": {
      const i = demoSnippets.findIndex((s) => s.id === str(a.id));
      if (i >= 0) demoSnippets.splice(i, 1);
      return null;
    }

    case "audit_query": {
      const source = str(a.source);
      const limit = num(a.limit, 300);
      return auditEntries.filter((e) => !source || e.source === source).slice(0, limit);
    }

    case "audit_count": {
      const source = str(a.source);
      return { total: auditEntries.filter((e) => !source || e.source === source).length };
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
        throwAppError("bad_param", "contentBase64 不是合法的 base64，写入已中止");
      }
      let text: string;
      try {
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

    case "fs_upload": {
      const remote = absPath(a.remotePath);
      const parent = remote.slice(0, remote.lastIndexOf("/")) || "/";
      if (!fsTree[parent]) {
        failTransfer(`演示模式：目录不存在：${parent}`);
      }
      const name = remote.slice(remote.lastIndexOf("/") + 1);
      return simulateTransfer(
        4_812_640,
        1500,
        name.includes("fail") ? "演示模式：模拟传输中断（连接被对端重置）" : null,
      );
    }

    case "fs_download": {
      const remote = absPath(a.remotePath);
      if (!demoFsEntryExists(remote)) {
        failTransfer(`演示模式：文件不存在：${remote}`);
      }
      const name = remote.slice(remote.lastIndexOf("/") + 1);
      return simulateTransfer(
        4_812_640,
        1500,
        name.includes("fail") ? "演示模式：模拟传输中断（连接被对端重置）" : null,
      );
    }

    case "fs_pack_download": {
      const remote = absPath(a.remotePath);
      if (!fsTree[remote]) {
        failTransfer(`演示模式：目录不存在：${remote}`);
      }
      const name = remote.slice(remote.lastIndexOf("/") + 1);
      return simulateTransfer(
        2_408_192,
        1200,
        name.includes("fail") ? "演示模式：模拟打包中断（连接被对端重置）" : null,
      );
    }

    case "fs_extract": {
      const p = absPath(a.path);
      return p.replace(/\.(tar\.gz|tgz|tar\.bz2|tbz2|tbz|tar\.xz|txz|tar|zip)$/i, "");
    }

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

    case "ai_chat": {
      jobSeq += 1;
      const jobId = `job-${jobSeq}`;
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
      activeAiJobs.set(jobId, liveConvId);
      streamAnswer(a.channel, jobId, str(a.message), Boolean(a.planMode));
      return { jobId, conversationId: liveConvId };
    }

    case "ai_cancel": {
      cancelAiJob(str(a.jobId));
      return null;
    }

    case "ai_steer": {
      const jobId = str(a.jobId);
      const message = str(a.message);
      if (!message.trim()) throwAppError("invalid_argument", "补充指令不能为空");
      if (cancelledAiJobs.has(jobId) || !aiChannels.has(jobId)) {
        throwAppError("internal", "AI 任务不存在或已结束");
      }
      pushEvent(aiChannels.get(jobId), { type: "steered", text: message });
      return null;
    }

    case "ai_edit_resend": {
      const conversationId = str(a.conversationId);
      const messages = conversationMessages[conversationId];
      const index = messages?.findIndex((m) => m.id === str(a.messageId)) ?? -1;
      if (!messages || index < 0) throwAppError("not_found", "消息不存在");
      const target = messages[index];
      if (target.role !== "user") throwAppError("invalid_argument", "只能编辑用户消息");
      for (const [jobId, convId] of activeAiJobs) {
        if (convId === conversationId) cancelAiJob(jobId);
      }
      messages.splice(index + 1);
      for (const run of demoRuns) {
        if (run.conversationId === conversationId && run.createdAt > target.createdAt) {
          run.status = "superseded";
        }
      }
      return null;
    }

    case "ai_confirm": {
      const jobId = str(a.jobId);
      const fn = pendingAi.get(jobId);
      pendingAi.delete(jobId);
      fn?.(str(a.decision, "allow"));
      return null;
    }

    case "ai_hitl_snapshot":
    case "ai_hitl_events":
      throwAppError("unsupported", "演示模式不提供 HITL 重连对账");

    case "ai_models":
      return ["deepseek-chat", "deepseek-reasoner", "gpt-4o-mini", "qwen-plus"];

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

    case "ai_circuit_status": {
      const id = str(a.id) || modelState.activeId;
      if (id === "m-deepseek") {
        return { consecutiveFailures: 5, openUntil: Date.now() + 5 * 60_000 };
      }
      if (id === "m-glm") {
        return { consecutiveFailures: 1, openUntil: null };
      }
      if (modelState.profiles.some((p) => p.id === id)) {
        return { consecutiveFailures: 0, openUntil: null };
      }
      throwAppError("not_found", `模型档案不存在: ${id}`);
    }

    case "ai_model_refresh":
      await new Promise((r) => window.setTimeout(r, 600));
      return { models: ["deepseek-chat", "deepseek-reasoner", "deepseek-coder"], malformed: 0 };

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
      return { modelsOk: true, modelsError: null, chatOk: true, chatError: null };

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
      const id = str(a.conversationId);
      return (conversationMessages[id] ?? []).map((m) => ({ ...m, conversationId: id }));
    }

    case "ai_run_list": {
      const conversationId = str(a.conversationId);
      const limit = num(a.limit, 0);
      const runs = demoRuns.filter((r) => !conversationId || r.conversationId === conversationId);
      return limit > 0 ? runs.slice(0, limit) : runs;
    }

    case "ai_run_events":
      return [];

    case "ai_usage_summary":
      return [
        { source: "chat", profileId: "m-deepseek", runs: 12, tokensIn: 48210, tokensOut: 9310, cacheCreationTokens: 1204, averageLatencyMs: 4120 },
        { source: "chat", profileId: "m-glm", runs: 3, tokensIn: 8204, tokensOut: 1502, cacheCreationTokens: 0, averageLatencyMs: 2860 },
        { source: "title", profileId: "m-deepseek", runs: 8, tokensIn: 2100, tokensOut: 96, cacheCreationTokens: 0, averageLatencyMs: 980 },
      ];

    case "ai_takeover_enter":
      return { token: DEMO_TAKEOVER_TOKEN };

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
      return { jobId, token: DEMO_TAKEOVER_TOKEN };
    }

    case "memory_create": {
      const scope = demoMemoryRequireScope((a.scope ?? {}) as { tenant?: string; subject?: string });
      const topic = str(a.topic).trim();
      const content = str(a.content);
      if (!topic || !content.trim()) throwAppError("bad_param", "invalid semantic memory input: invalid topic or content");
      const sanitized = demoMemorySanitize(content, str(a.secrets, "reject"));
      const now = Date.now();
      const entry: DemoMemoryEntry = {
        id: uid("mem"),
        tenant: scope.tenant,
        subject: scope.subject,
        topic,
        content: sanitized.content,
        version: 1,
        redacted: sanitized.redacted,
        createdAt: now,
        updatedAt: now,
      };
      demoMemoryEntries.push(entry);
      const { tenant: _t, subject: _s, ...dto } = entry;
      return dto;
    }

    case "memory_get": {
      const scope = demoMemoryRequireScope((a.scope ?? {}) as { tenant?: string; subject?: string });
      const entry = demoMemoryFind(scope, str(a.id));
      const { tenant: _t, subject: _s, ...dto } = entry;
      return dto;
    }

    case "memory_edit": {
      const scope = demoMemoryRequireScope((a.scope ?? {}) as { tenant?: string; subject?: string });
      const entry = demoMemoryFind(scope, str(a.id));
      const expectedVersion = num(a.expectedVersion);
      if (!expectedVersion) throwAppError("bad_param", "invalid semantic memory input: expected version must be positive");
      demoMemoryCAS(entry, expectedVersion);
      const hasTopic = typeof a.topic === "string";
      const hasContent = typeof a.content === "string";
      if (!hasTopic && !hasContent) throwAppError("bad_param", "invalid semantic memory input: edit requires topic or content");
      if (hasTopic) {
        const topic = (a.topic as string).trim();
        if (!topic) throwAppError("bad_param", "invalid semantic memory input: invalid topic");
        entry.topic = topic;
      }
      if (hasContent) {
        const content = a.content as string;
        if (!content.trim()) throwAppError("bad_param", "invalid semantic memory input: invalid content");
        const sanitized = demoMemorySanitize(content, str(a.secrets, "reject"));
        entry.content = sanitized.content;
        entry.redacted = sanitized.redacted;
      }
      entry.version += 1;
      entry.updatedAt = Date.now();
      const { tenant: _t, subject: _s, ...dto } = entry;
      return dto;
    }

    case "memory_delete": {
      const scope = demoMemoryRequireScope((a.scope ?? {}) as { tenant?: string; subject?: string });
      const entry = demoMemoryFind(scope, str(a.id));
      const expectedVersion = num(a.expectedVersion);
      if (!expectedVersion) throwAppError("bad_param", "invalid semantic memory input: expected version must be positive");
      demoMemoryCAS(entry, expectedVersion);
      demoMemoryEntries.splice(demoMemoryEntries.indexOf(entry), 1);
      return null;
    }

    case "memory_index": {
      const scope = demoMemoryRequireScope((a.scope ?? {}) as { tenant?: string; subject?: string });
      const topics = new Map<string, { id: string; version: number; redacted: boolean; updatedAt: number }[]>();
      for (const entry of demoMemoryEntries) {
        if (entry.tenant !== scope.tenant || entry.subject !== scope.subject) continue;
        const list = topics.get(entry.topic) ?? [];
        list.push({ id: entry.id, version: entry.version, redacted: entry.redacted, updatedAt: entry.updatedAt });
        topics.set(entry.topic, list);
      }
      return [...topics.entries()]
        .sort(([x], [y]) => x.localeCompare(y))
        .map(([topic, entries]) => ({ topic, entries }));
    }

    case "memory_settings_get": {
      const scope = demoMemoryRequireScope((a.scope ?? {}) as { tenant?: string; subject?: string });
      const settings = demoMemorySettings.get(demoMemoryScopeKey(scope.tenant, scope.subject));
      return settings ?? { injectionEnabled: false, toolsEnabled: false, version: 0 };
    }

    case "memory_settings_set": {
      const scope = demoMemoryRequireScope((a.scope ?? {}) as { tenant?: string; subject?: string });
      const key = demoMemoryScopeKey(scope.tenant, scope.subject);
      const current = demoMemorySettings.get(key) ?? { injectionEnabled: false, toolsEnabled: false, version: 0 };
      if (typeof a.injectionEnabled !== "boolean" && typeof a.toolsEnabled !== "boolean") {
        throwAppError("bad_param", "invalid semantic memory input: settings update requires at least one flag");
      }
      demoMemoryCAS({ id: "memory-settings", version: current.version }, num(a.expectedVersion));
      const next: DemoMemorySettings = {
        injectionEnabled: typeof a.injectionEnabled === "boolean" ? a.injectionEnabled : current.injectionEnabled,
        toolsEnabled: typeof a.toolsEnabled === "boolean" ? a.toolsEnabled : current.toolsEnabled,
        version: current.version + 1,
      };
      demoMemorySettings.set(key, next);
      return next;
    }

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

    case "vault_set_autolock": {
      const minutes = a.minutes;
      if (typeof minutes !== "number" || !Number.isInteger(minutes) || minutes < 0 || minutes > 1440) {
        throwAppError("bad_param", "参数错误: 自动锁时长需在 0（禁用）至 1440 分钟之间");
      }
      vaultState.autoLockMinutes = minutes;
      return null;
    }

    case "vault_change_password":
      return null;

    case "vault_set_credential": {
      const src: "inline" | "file" = a.source === "file" ? "file" : "inline";
      const existing = a.id ? demoCredentials.find((c) => c.id === str(a.id)) : undefined;
      if (existing) {
        if (typeof a.name === "string") existing.name = a.name;
        if (typeof a.kind === "string") existing.kind = a.kind;
        if (typeof a.secret === "string") existing.secret = a.secret;
        if (existing.kind === "private_key") {
          existing.source = src;
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
          if (c.kind === "private_key" && typeof a.source === "string") {
            c.source = a.source === "file" ? "file" : "inline";
          }
        }
        if (c.kind === "private_key" && typeof a.passphrase === "string") {
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
      for (const x of assets) if (x.credId === str(a.id)) x.credId = null;
      return null;
    }

    case "vault_reveal_credential": {
      if (!vaultState.unlocked) throw new Error("凭据库已锁定，请先解锁");
      const c = demoCredentials.find((x) => x.id === str(a.id));
      if (!c) throw new Error("找不到这条凭据");
      if (c.kind === "private_key") {
        const isRef = c.source === "file";
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

    case "forward_create_remote": {
      const bindHost = str(a.bindHost).trim() || "127.0.0.1";
      if (!isLoopbackHost(bindHost) && a.acknowledgeRisk !== true) {
        throwAppError(
          "needs_confirm",
          "远程转发没有认证，远端监听非回环地址会把本地服务暴露给远端网络，必须确认开放风险",
          { risk: "unauthenticated_exposed_remote_forward", listenHost: bindHost, authentication: "none" },
        );
      }
      const f = {
        id: uid("f"),
        sessionId: str(a.sessionId),
        listenHost: bindHost,
        listenPort: num(a.bindPort, 18080),
        targetHost: str(a.targetHost, "127.0.0.1"),
        targetPort: num(a.targetPort, 3306),
        kind: "remote",
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

    case "sync_digest":
      return {
        origin: "demo",
        protocol: 1,
        appVersion: "0.1.0-demo",
        desktop: false,
        assets: assets
          .filter((x) => !x.builtin)
          .map((x) => ({
            id: x.id,
            name: x.name,
            kind: x.kind,
            host: x.host,
            username: x.username,
            updatedAt: x.updatedAt,
            deletedAt: x.deletedAt,
            hasCred: !!x.credId,
            groupId: x.groupId,
          })),
      };

    case "sync_export": {
      const ids = new Set(Array.isArray(a.assetIds) ? (a.assetIds as string[]) : []);
      const withCreds = a.withCreds === true;
      const bundle: {
        protocol: number;
        origin: string;
        exportedAt: number;
        groups: Record<string, unknown>[];
        assets: Record<string, unknown>[];
        creds: Record<string, unknown>[];
        snippets: Record<string, unknown>[];
        warnings: string[];
      } = { protocol: 1, origin: "demo", exportedAt: Date.now(), groups: [], assets: [], creds: [], snippets: [], warnings: [] };
      const includedGroups = new Set<string>();
      const collectGroupAncestors = (groupId: string | null) => {
        const chain: (typeof groups)[number][] = [];
        let cursor = groupId;
        const visiting = new Set<string>();
        while (cursor && !includedGroups.has(cursor)) {
          if (visiting.has(cursor)) {
            bundle.warnings.push("分组祖先链存在循环，已停止继续向上收集");
            break;
          }
          visiting.add(cursor);
          const g = groups.find((x) => x.id === cursor);
          if (!g) {
            bundle.warnings.push(`分组 ${cursor} 不存在，祖先链在此处停止`);
            break;
          }
          chain.push(g);
          cursor = g.parentId;
        }
        for (const g of chain.reverse()) {
          bundle.groups.push({
            id: g.id,
            parentId: g.parentId,
            name: g.name,
            sort: g.sort,
            createdAt: g.createdAt,
            updatedAt: g.updatedAt,
          });
          includedGroups.add(g.id);
        }
      };
      for (const asset of assets) {
        if (!ids.has(asset.id) || asset.builtin) continue;
        collectGroupAncestors(asset.groupId);
        bundle.assets.push({
          id: asset.id,
          groupId: asset.groupId,
          kind: asset.kind,
          name: asset.name,
          host: asset.host,
          port: asset.port,
          username: asset.username,
          authKind: asset.authKind,
          keyPath: asset.keyPath,
          credId: asset.credId,
          optionsJson: JSON.stringify(asset.options ?? {}),
          tags: asset.tags,
          note: asset.note,
          sort: asset.sort,
          createdAt: asset.createdAt,
          updatedAt: asset.updatedAt,
          deletedAt: asset.deletedAt,
        });
      }
      if (withCreds) {
        const seen = new Set<string>();
        for (const payload of bundle.assets) {
          const credId = typeof payload.credId === "string" ? payload.credId : "";
          if (!credId || seen.has(credId)) continue;
          seen.add(credId);
          const cred = demoCredentials.find((c) => c.id === credId);
          if (!cred) {
            bundle.warnings.push(`资产 ${String(payload.id)} 引用的凭据 ${credId} 不存在`);
            continue;
          }
          if (!vaultState.unlocked) throwAppError("vault_locked", "凭据库已锁定，请先解锁");
          bundle.creds.push({ id: cred.id, name: cred.name, kind: cred.kind, secret: cred.secret, updatedAt: cred.updatedAt });
        }
      }
      for (const snippet of demoSnippets) {
        collectGroupAncestors(snippet.groupId);
        bundle.snippets.push({
          id: snippet.id,
          groupId: snippet.groupId,
          name: snippet.name,
          body: snippet.body,
          sort: snippet.sort,
          createdAt: snippet.createdAt,
          updatedAt: snippet.updatedAt,
        });
      }
      return bundle;
    }

    case "sync_import": {
      const bundle = a.bundle as Record<string, unknown> | undefined;
      if (!bundle || typeof bundle !== "object" || Array.isArray(bundle)) {
        throwAppError("bad_param", "资产包格式不正确");
      }
      if (bundle.protocol !== 1) {
        throwAppError("unsupported", `不支持的同步协议版本 ${String(bundle.protocol)}（当前支持 1）`);
      }
      const bundleCreds = Array.isArray(bundle.creds) ? bundle.creds : [];
      if (bundleCreds.length > 0 && !vaultState.unlocked) {
        throwAppError("vault_locked", "凭据库已锁定，请先解锁");
      }
      const force = a.force === true;
      const report = {
        groupsCreated: 0,
        groupsUpdated: 0,
        assetsCreated: 0,
        assetsUpdated: 0,
        credsCreated: 0,
        credsUpdated: 0,
        credsDeleted: 0,
        snippetsCreated: 0,
        snippetsUpdated: 0,
        skippedNewer: 0,
        skippedNewerDetails: [] as {
          kind: string;
          id: string;
          name: string;
          localRevision: number;
          remoteRevision: number;
          equalRevision: boolean;
        }[],
        refused: 0,
        warnings: Array.isArray(bundle.warnings)
          ? (bundle.warnings as unknown[]).filter((w): w is string => typeof w === "string")
          : [],
      };
      const bundleAssets = Array.isArray(bundle.assets) ? bundle.assets : [];
      const effectiveRevision = (updatedAt: unknown, deletedAt: unknown): number =>
        Math.max(num(updatedAt, 0), typeof deletedAt === "number" ? deletedAt : 0);
      const assetDecisions = (bundleAssets as Record<string, unknown>[]).map((p) => {
        const id = str(p?.id).trim();
        const existing = assets.find((x) => x.id === id);
        if (existing?.builtin) {
          return { acceptance: "refused" as const, warning: "内置「当前设备」不接受同步覆盖" };
        }
        if (!id || !str(p.name).trim()) {
          return {
            acceptance: "refused" as const,
            warning: `资产 ${id || "(空)"} 的 ID 或名称不合法，已拒绝导入`,
          };
        }
        const localRevision = existing
          ? effectiveRevision(existing.updatedAt, existing.deletedAt)
          : 0;
        const remoteRevision = effectiveRevision(p.updatedAt, p.deletedAt);
        if (existing && !force && localRevision > remoteRevision) {
          return {
            acceptance: "skipped" as const,
            warning: `资产 ${id} 的本机版本较新，已跳过；如需覆盖请使用强制同步`,
            localRevision,
            remoteRevision,
            equalRevision: localRevision === remoteRevision,
          };
        }
        return { acceptance: "accepted" as const, warning: "" };
      });
      const blockedCredIds = new Set<string>();
      (bundleAssets as Record<string, unknown>[]).forEach((p, i) => {
        const credId = str(p?.credId).trim();
        if (credId && assetDecisions[i].acceptance !== "accepted") blockedCredIds.add(credId);
      });
      const bundleGroups = Array.isArray(bundle.groups) ? bundle.groups : [];
      for (const g of bundleGroups as Record<string, unknown>[]) {
        const id = str(g?.id).trim();
        if (!id) {
          report.refused += 1;
          report.warnings.push("拒绝了 ID 为空的分组");
          continue;
        }
        const existing = groups.find((x) => x.id === id);
        const parentId = str(g.parentId).trim();
        const row = {
          id,
          parentId: parentId || null,
          name: str(g.name, "分组"),
          sort: num(g.sort, 0),
          createdAt: num(g.createdAt, Date.now()),
          updatedAt: num(g.updatedAt, Date.now()),
        };
        if (existing) {
          Object.assign(existing, row);
          report.groupsUpdated += 1;
        } else {
          groups.push(row);
          report.groupsCreated += 1;
        }
      }
      for (const c of bundleCreds as Record<string, unknown>[]) {
        const id = str(c?.id).trim();
        if (!id) {
          report.refused += 1;
          report.warnings.push("拒绝了 ID 为空的凭据");
          continue;
        }
        if (blockedCredIds.has(id)) {
          report.warnings.push(`凭据 ${id} 关联的资产因本机版本较新或导入被拒而受到保护，本机凭据保持不变`);
          continue;
        }
        const existing = demoCredentials.find((x) => x.id === id);
        const secret = str(c.secret);
        if (existing) {
          existing.name = str(c.name, existing.name);
          existing.kind = str(c.kind, existing.kind);
          existing.secret = secret;
          existing.updatedAt = Date.now();
          report.credsUpdated += 1;
        } else {
          demoCredentials.push({
            id,
            name: str(c.name, "凭据"),
            kind: str(c.kind, "password"),
            secret,
            createdAt: Date.now(),
            updatedAt: Date.now(),
          });
          report.credsCreated += 1;
        }
      }
      (bundleAssets as Record<string, unknown>[]).forEach((p, i) => {
        const decision = assetDecisions[i];
        if (decision.acceptance !== "accepted") {
          if (decision.acceptance === "skipped") {
            report.skippedNewer += 1;
            report.skippedNewerDetails.push({
              kind: "asset",
              id: str(p?.id).trim(),
              name: str(p?.name),
              localRevision: decision.localRevision,
              remoteRevision: decision.remoteRevision,
              equalRevision: decision.equalRevision,
            });
          } else {
            report.refused += 1;
          }
          report.warnings.push(decision.warning);
          return;
        }
        const id = str(p.id).trim();
        const existing = assets.find((x) => x.id === id);
        const incomingUpdated = num(p.updatedAt, 0);
        const credId = str(p.credId).trim();
        const groupId = str(p.groupId).trim();
        let options: Record<string, unknown> = {};
        if (typeof p.optionsJson === "string") {
          try {
            const parsed = JSON.parse(p.optionsJson);
            if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) options = parsed;
          } catch {
            report.warnings.push(`资产 ${id} 的 optionsJson 不是合法 JSON，已按空处理`);
          }
        }
        const row = {
          id,
          groupId: groupId || null,
          kind: str(p.kind, "ssh"),
          name: str(p.name, "资产"),
          host: typeof p.host === "string" ? p.host : null,
          port: typeof p.port === "number" ? p.port : null,
          username: typeof p.username === "string" ? p.username : null,
          authKind: typeof p.authKind === "string" ? p.authKind : "password",
          keyPath: str(p.keyPath).trim() || null,
          credId: credId && demoCredentials.some((c) => c.id === credId) ? credId : null,
          options,
          tags: str(p.tags, ""),
          note: str(p.note, ""),
          sort: num(p.sort, 0),
          createdAt: num(p.createdAt, Date.now()),
          updatedAt: incomingUpdated || Date.now(),
          deletedAt: typeof p.deletedAt === "number" ? p.deletedAt : null,
          builtin: false,
        };
        if (credId && !row.credId) {
          report.warnings.push(`资产 ${id} 引用的凭据 ${credId} 不存在，已清除该引用`);
        }
        if (row.groupId && !groups.some((g) => g.id === row.groupId)) {
          report.warnings.push(`资产 ${id} 引用的分组 ${row.groupId} 不存在，已清除该引用`);
          row.groupId = null;
        }
        if (existing) {
          Object.assign(existing, row);
          report.assetsUpdated += 1;
        } else {
          assets.push(row);
          report.assetsCreated += 1;
        }
      });
      const bundleTombstones = Array.isArray(bundle.credTombstones) ? bundle.credTombstones : [];
      for (const t of bundleTombstones as Record<string, unknown>[]) {
        const id = str(t?.id).trim();
        if (!id) {
          report.refused += 1;
          report.warnings.push("拒绝了 ID 为空的凭据删除墓碑");
          continue;
        }
        const local = demoCredentials.find((c) => c.id === id);
        if (!local) continue;
        const remoteRevision = num(t.deletedAt, 0);
        if (!force && local.updatedAt > remoteRevision) {
          report.skippedNewer += 1;
          report.skippedNewerDetails.push({
            kind: "credential",
            id,
            name: local.name,
            localRevision: local.updatedAt,
            remoteRevision,
            equalRevision: false,
          });
          report.warnings.push(`凭据 ${id} 的本机版本较新，已忽略远端删除墓碑；如需覆盖请使用强制同步`);
          continue;
        }
        demoCredentials.splice(demoCredentials.indexOf(local), 1);
        report.credsDeleted += 1;
      }
      const bundleSnippets = Array.isArray(bundle.snippets) ? bundle.snippets : [];
      for (const p of bundleSnippets as Record<string, unknown>[]) {
        const id = str(p?.id).trim();
        if (!id || !str(p.name).trim()) {
          report.refused += 1;
          report.warnings.push(`片段 ${id || "(空)"} 的 ID 或名称不合法，已拒绝导入`);
          continue;
        }
        const existing = demoSnippets.find((s) => s.id === id);
        const incomingUpdated = num(p.updatedAt, 0);
        if (existing && !force && existing.updatedAt > incomingUpdated) {
          report.skippedNewer += 1;
          report.skippedNewerDetails.push({
            kind: "snippet",
            id,
            name: existing.name,
            localRevision: existing.updatedAt,
            remoteRevision: incomingUpdated,
            equalRevision: false,
          });
          report.warnings.push(`片段 ${id} 的本机版本较新，已跳过；如需覆盖请使用强制同步`);
          continue;
        }
        const groupId = str(p.groupId).trim();
        const row: DemoSnippet = {
          id,
          groupId: groupId || null,
          name: str(p.name, "片段"),
          body: str(p.body),
          sort: num(p.sort, 0),
          createdAt: num(p.createdAt, Date.now()),
          updatedAt: incomingUpdated || Date.now(),
        };
        if (row.groupId && !groups.some((g) => g.id === row.groupId)) {
          report.warnings.push(`片段 ${id} 引用的分组 ${row.groupId} 不存在，已清除该引用`);
          row.groupId = null;
        }
        if (existing) {
          Object.assign(existing, row);
          report.snippetsUpdated += 1;
        } else {
          demoSnippets.push(row);
          report.snippetsCreated += 1;
        }
      }
      return report;
    }

    case "sync_bundle_read": {
      const path = str(a.path).trim();
      if (!path) throwAppError("bad_param", "资产包路径为空");
      const file = bundleFiles.get(path);
      if (!file) throwAppError("bad_param", `无法读取资产包文件: ${path}`);
      if (file.encrypted && str(a.password) === "") {
        throwAppError("bad_param", "资产包已加密，请提供口令");
      }
      if (file.encrypted && str(a.password) !== file.password) {
        throwAppError("decrypt", "资产包解密失败：口令错误或数据已被篡改");
      }
      return file.content;
    }

    case "sync_bundle_write": {
      const path = str(a.path).trim();
      if (!path) throwAppError("bad_param", "资产包路径为空");
      const content = str(a.content);
      if (content.length > 8 << 20) throwAppError("bad_param", "资产包内容超过 8 MiB，拒绝写入");
      const password = str(a.password);
      bundleFiles.set(path, { content, encrypted: password !== "", password });
      return password !== ""
        ? { encrypted: true }
        : { encrypted: false, warning: "资产包以明文导出，获得文件的人都能直接读取其中的凭据，请妥善保管" };
    }

    case "sync_token":
    case "sync_token_list":
      return null;

    case "sync_token_rotate":
      if (str(a.id) !== "") throwAppError("unsupported", "该同步操作只在服务端可用");
      return null;

    case "sync_token_issue":
    case "sync_token_revoke":
      throwAppError("unsupported", "该同步操作只在服务端可用");

    case "ssh_import_preview": {
      const source = str(a.source) === "termius" ? "termius" : "ssh-config";
      if (source === "termius" && a.confirmed !== true) {
        throwAppError("bad_param", "参数错误: 读取本机 Termius 数据需要显式确认");
      }
      const canned = demoSshImportPreviews[source];
      return {
        ...canned,
        hosts: canned.hosts.map((h) => ({ ...h, identityFiles: [...h.identityFiles], warnings: [...h.warnings] })),
        keys: canned.keys.map((k) => ({ ...k, aliases: [...k.aliases], warnings: [...k.warnings] })),
        diagnostics: [...canned.diagnostics],
      };
    }

    case "ssh_import_apply": {
      const source = str(a.source) === "termius" ? "termius" : "ssh-config";
      if (source === "termius" && a.confirmed !== true) {
        throwAppError("bad_param", "参数错误: 读取本机 Termius 数据需要显式确认");
      }
      if (!vaultState.unlocked) throwAppError("vault_locked", "凭据库已锁定，请先解锁再导入");
      const canned = demoSshImportPreviews[source];
      const hostActions = new Map(
        (Array.isArray(a.hosts) ? a.hosts : []).map((h) => {
          const item = (h ?? {}) as Record<string, unknown>;
          return [str(item.id), str(item.action) || "skip"];
        }),
      );
      const keyActions = new Map(
        (Array.isArray(a.keys) ? a.keys : []).map((k) => {
          const item = (k ?? {}) as Record<string, unknown>;
          return [str(item.id), str(item.action) || "skip"];
        }),
      );
      const result = {
        assetsCreated: 0,
        assetsUpdated: 0,
        credentialsCreated: 0,
        credentialsUpdated: 0,
        skipped: 0,
        warnings: [] as string[],
      };
      const credIDByKeyID = new Map<string, string>();
      const overwrittenCredIDs = new Set<string>();
      for (const key of canned.keys) {
        const action = keyActions.get(key.id) ?? "skip";
        const name = key.aliases[0] ?? key.id;
        if (action === "import" && key.action === "add") {
          const cred: DemoCredential = {
            id: uid("cred"),
            name,
            kind: "private_key",
            source: key.source === "termius" ? "inline" : "file",
            secret: key.source === "termius" ? demoInlinePem : key.path,
            createdAt: Date.now(),
            updatedAt: Date.now(),
          };
          demoCredentials.push(cred);
          credIDByKeyID.set(key.id, cred.id);
          result.credentialsCreated += 1;
        } else if (action === "overwrite" && key.action === "conflict-alias") {
          const existing = demoCredentials.find((c) => c.name.toLowerCase() === name.toLowerCase());
          if (existing && overwrittenCredIDs.has(existing.id)) {
            result.warnings.push(`凭据 "${name}" 已在本次导入中被覆盖，跳过重复的覆盖`);
            result.skipped += 1;
          } else if (existing) {
            existing.secret = key.source === "termius" ? demoInlinePem : key.path;
            if (existing.kind === "private_key") {
              existing.source = key.source === "termius" ? "inline" : "file";
            }
            existing.updatedAt = Date.now();
            for (const [keyID, credID] of credIDByKeyID) {
              if (credID === existing.id) credIDByKeyID.delete(keyID);
            }
            credIDByKeyID.set(key.id, existing.id);
            overwrittenCredIDs.add(existing.id);
            result.credentialsUpdated += 1;
          } else {
            result.skipped += 1;
          }
        } else {
          result.skipped += 1;
        }
      }
      for (const host of canned.hosts) {
        const action = hostActions.get(host.id) ?? "skip";
        if (action === "import" && host.action === "add") {
          const candidateKeyIDs: string[] = [];
          if (host.keyName) {
            const named = canned.keys.find(
              (k) => k.aliases.length > 0 && k.aliases[0].toLowerCase() === host.keyName.toLowerCase(),
            );
            if (named) candidateKeyIDs.push(named.id);
          }
          for (const path of host.identityFiles) {
            const byPath = canned.keys.find((k) => k.path === path);
            if (byPath) candidateKeyIDs.push(byPath.id);
          }
          let credId: string | null = null;
          for (const keyID of candidateKeyIDs) {
            if (credIDByKeyID.has(keyID)) {
              credId = credIDByKeyID.get(keyID)!;
              break;
            }
          }
          const wantsKey = candidateKeyIDs.length > 0 || host.identityFiles.length > 0;
          assets.push({
            id: uid("a"),
            groupId: null,
            kind: "ssh",
            name: host.alias,
            host: host.hostname,
            port: host.port,
            username: host.username,
            authKind: wantsKey ? "key" : host.authMethod === "password" ? "password" : "agent",
            keyPath: credId === null && host.identityFiles.length > 0 ? host.identityFiles[0]! : null,
            credId,
            options: {},
            tags: "",
            note: "",
            sort: 0,
            createdAt: Date.now(),
            updatedAt: Date.now(),
            deletedAt: null,
            builtin: false,
          });
          result.assetsCreated += 1;
        } else if (action === "overwrite" && host.action === "conflict-alias") {
          const existing = assets.find(
            (x) => x.name.toLowerCase() === host.alias.toLowerCase() && x.deletedAt === null,
          );
          if (existing) {
            existing.host = host.hostname;
            existing.port = host.port;
            existing.username = host.username;
            existing.updatedAt = Date.now();
            result.assetsUpdated += 1;
          } else {
            result.skipped += 1;
          }
        } else {
          result.skipped += 1;
        }
      }
      return result;
    }

    case "vault_generate_key": {
      const name = str(a.name).trim();
      if (!name) throwAppError("bad_param", "参数错误: 密钥名称不能为空");
      if (demoCredentials.some((c) => c.name.toLowerCase() === name.toLowerCase())) {
        throwAppError("bad_param", `参数错误: 已存在同名凭据 "${name}"`);
      }
      if (!vaultState.unlocked) throwAppError("vault_locked", "凭据库已锁定，请先解锁");
      const algorithm = str(a.algorithm, "ed25519") === "rsa" ? "rsa" : "ed25519";
      const cred: DemoCredential = {
        id: uid("cred"),
        name,
        kind: "private_key",
        source: "inline",
        secret: demoInlinePem,
        createdAt: Date.now(),
        updatedAt: Date.now(),
      };
      if (typeof a.passphrase === "string" && a.passphrase.trim()) {
        cred.passphrase = a.passphrase;
      }
      demoCredentials.push(cred);
      return {
        id: cred.id,
        name,
        algorithm,
        fingerprint: `SHA256:demo${algorithm === "rsa" ? "Rsa" : "Ed25519"}Fingerprint0000000000`,
        publicKey: `${algorithm === "rsa" ? "ssh-rsa" : "ssh-ed25519"} AAAAC3NzaC1lZDI1NTE5AAAAIDemoOnlyNotARealKey000000000000 demo@${name}`,
      };
    }

    case "files_settings_get":
      return { ...demoFilesSettings };

    case "files_settings_set": {
      const value = str(a.publicBaseURL).trim();
      if (value !== "") {
        let valid = false;
        try {
          const parsed = new URL(value);
          valid =
            (parsed.protocol === "http:" || parsed.protocol === "https:") &&
            parsed.host !== "" &&
            parsed.username === "" &&
            parsed.password === "" &&
            parsed.search === "" &&
            !value.includes("?") &&
            parsed.hash === "";
        } catch {
          valid = false;
        }
        if (!valid) {
          throwAppError("bad_param", "参数错误: 文件访问基础 URL 需要是 http(s) 地址且不含查询参数或片段");
        }
      }
      demoFilesSettings.publicBaseURL = value.replace(/\/+$/, "");
      return { ...demoFilesSettings };
    }

    case "files_save_image": {
      const path = str(a.path);
      if (!path) throwAppError("bad_param", "参数错误: 保存路径不能为空");
      const content = str(a.contentBase64);
      if (!content) throwAppError("bad_param", "参数错误: 图片内容不能为空");
      const padding = content.endsWith("==") ? 2 : content.endsWith("=") ? 1 : 0;
      return { path, bytes: Math.floor((content.length * 3) / 4) - padding };
    }

    default:
      return null;
  }
}
