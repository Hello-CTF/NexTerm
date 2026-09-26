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
  fakeQuery,
  forwards,
  fsFileContent,
  fsTree,
  groups,
  images,
  mcpTools,
  mounts,
  mysqlColumns,
  mysqlIndexes,
  mysqlSchemas,
  mysqlTables,
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
let jobSeq = 0;

function newTabId(prefix = "t"): string {
  return `${prefix}-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
}

/** 演示模式下没有任何真实凭据，任何 secret 一律回显成掩码。 */
const MASK = "••••••••••••";

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

function streamAnswer(channel: unknown, jobId: string, question: string) {
  const { answer, commands } = answerFor(question);
  const yesNo = question.includes("重启") || question.includes("restart") || question.includes("删除");

  let delay = 260;

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
      });
      pendingAi.set(jobId, (decision) => {
        if (decision === "deny") {
          pushEvent(channel, { type: "delta", text: "已取消，没有执行任何写操作。" });
        } else {
          pushEvent(channel, { type: "delta", text: "已按你的授权执行 `docker restart mysql-prod`，容器已重启：\n\n" });
          pushEvent(channel, { type: "delta", text: commands.map((c) => `  $ ${c}\n`).join("") + "\n" });
        }
        finish();
      });
      return;
    }
    commands.forEach((cmd, i) => {
      later(240 + i * 260, () =>
        pushEvent(channel, {
          type: "toolResult",
          summary: `$ ${cmd}\n${toolOutputFor(cmd)}`,
          ok: true,
          exitCode: 0,
        }),
      );
    });
    delay = 240 + commands.length * 260 + 200;
    answer.split("\n").forEach((line, i) => {
      later(delay + i * 70, () => pushEvent(channel, { type: "delta", text: `${line}\n` }));
    });
    later(delay + answer.split("\n").length * 70 + 120, finish);
  });

  function finish() {
    later(0, () => {
      emit("session://status", { sessionId: "s-web01", status: "connected", error: null });
      pushEvent(channel, {
        type: "done",
        answer: commands.map((c) => `$ ${c}`).join("\n") + "\n—— 以上命令都已在你面前的终端里跑过，可回放。",
      });
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
    return containerLogLines("api-server").slice(-6).join("\n");
  }
  if (cmd.startsWith("docker exec redis")) {
    return "used_memory:50544640\nused_memory_human:48.20M\nmaxmemory:536870912\nmaxmemory_policy:allkeys-lru";
  }
  if (cmd.startsWith("nginx -t")) {
    return "nginx: configuration file /etc/nginx/nginx.conf test is successful";
  }
  if (cmd.startsWith("cat /etc/nginx")) {
    return fsFileContent["/etc/nginx/nginx.conf"].split("\n").slice(0, 12).join("\n") + "\n…（共 46 行）";
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
      if (i >= 0) sessions.splice(i, 1);
      return null;
    }

    case "session_probe":
      return { open: true };

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
      const shell = new DemoShell((text) => pushText(a.channel, text.replace(/\n/g, "\r\n")));
      shells.set(tabId, shell);
      const asset = assets.find((x) => x.id === sessions.find((s) => s.id === str(a.sessionId))?.assetId);
      later(90, () => {
        pushText(a.channel, `\r\n\x1b[2m[演示模式] 已连到 ${asset?.name ?? "web-01"}（假数据，随便敲）\x1b[0m\r\n\r\n`);
        shell.start();
      });
      return tabId;
    }

    case "terminal_write": {
      const shell = shells.get(str(a.tabId));
      const data = a.data;
      if (shell && Array.isArray(data)) {
        shell.input(new TextDecoder().decode(Uint8Array.from(data as number[])));
      }
      return null;
    }

    case "terminal_resize":
    case "terminal_set_visible":
    case "terminal_switch_encoding":
      return null;

    case "terminal_detach": {
      shells.delete(str(a.tabId));
      logTimers.get(str(a.tabId))?.();
      logTimers.delete(str(a.tabId));
      return null;
    }

    case "terminal_close_tab": {
      shells.delete(str(a.tabId));
      logTimers.get(str(a.tabId))?.();
      logTimers.delete(str(a.tabId));
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
        groupId: null,
        kind: str(a.kind, "ssh"),
        name: str(a.name, "新资产"),
        host: (a.host as string | null) ?? null,
        port: (a.port as number | null) ?? null,
        username: (a.username as string | null) ?? null,
        authKind: (a.authKind as string | null) ?? "password",
        keyPath: (a.keyPath as string | null) ?? null,
        credId: (a.credId as string | null) ?? null,
        options: {},
        tags: "",
        note: "",
        sort: 99,
        createdAt: Date.now(),
        updatedAt: Date.now(),
        deletedAt: null,
      };
      assets.push(created);
      return { ...created };
    }

    case "asset_update": {
      const target = assets.find((x) => x.id === str(a.id));
      if (target) Object.assign(target, a, { updatedAt: Date.now() });
      return { ...(target ?? assets[0]) };
    }

    case "asset_delete": {
      const target = assets.find((x) => x.id === str(a.id));
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
      let b64 = "";
      const chunk = 0x8000;
      for (let i = 0; i < bytes.length; i += chunk) {
        b64 += btoa(String.fromCharCode(...bytes.subarray(i, i + chunk)));
      }
      return { path, size: bytes.length, contentBase64: b64 };
    }

    case "fs_write": {
      const path = absPath(a.path);
      const b64 = str(a.contentBase64);
      try {
        const bin = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
        fsFileContent[path] = new TextDecoder().decode(bin);
      } catch {
        // 忽略解码失败，演示模式下不阻塞保存流程
      }
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
      streamAnswer(a.channel, jobId, str(a.message));
      return { jobId };
    }

    case "ai_cancel":
      pendingAi.delete(str(a.jobId));
      return null;

    case "ai_confirm": {
      const jobId = str(a.jobId);
      const fn = pendingAi.get(jobId);
      pendingAi.delete(jobId);
      fn?.(str(a.decision, "allow"));
      return null;
    }

    case "ai_models":
      return ["deepseek-chat", "deepseek-reasoner", "gpt-4o-mini", "qwen-plus"];

    case "ai_presets":
      return [...providerPresets];

    case "ai_get_provider":
      return { ...providerConfig, apiKey: providerConfig.apiKey ? MASK : "" };

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

    case "ai_messages":
      return [];

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
      return { initialized: true, mode: "master", unlocked: true, autoLockMinutes: 30 };

    case "vault_init_master":
    case "vault_init_dpapi":
    case "vault_unlock":
    case "vault_lock":
    case "vault_change_password":
    case "vault_delete_credential":
      return null;

    case "vault_set_credential":
    case "credential_save":
      return { id: uid("cred") };

    case "vault_list_credentials":
      return [
        { id: "cred1", name: "web-01-password", kind: "password", cipher: "xchacha20poly1305", kekHint: "master", createdAt: Date.now() - 20 * 86_400_000, updatedAt: Date.now() - 86_400_000 },
        { id: "cred2", name: "nat-01-password", kind: "password", cipher: "xchacha20poly1305", kekHint: "master", createdAt: Date.now() - 25 * 86_400_000, updatedAt: Date.now() - 3 * 86_400_000 },
      ];

    case "vault_reveal_credential":
      return MASK;

    /* ─────────────── MCP ─────────────── */
    case "mcp_get_settings":
      return { enabled: true, httpPort: 8799, token: "nx_" + "a1b2c3d4e5f6".repeat(3), writeTools: {} };

    case "mcp_save_settings":
      return false;

    case "mcp_generate_token":
      return "nx_" + Math.random().toString(36).slice(2).repeat(2).slice(0, 32);

    case "mcp_list_tools":
      return mcpTools.map((t) => ({ ...t }));

    case "mcp_client_config_snippet":
      return {
        mcpServers: {
          nexterm: {
            type: "http",
            url: "http://127.0.0.1:8799/mcp",
            headers: { Authorization: "Bearer <your-token>" },
          },
        },
      };

    case "mcp_write_client_config": {
      const target = str(a.target, "claude_code");
      const paths: Record<string, string> = {
        claude_code: "~/.claude.json",
        claude_desktop: "~/Library/Application Support/Claude/claude_desktop_config.json",
        cursor: "~/.cursor/mcp.json",
      };
      return {
        target,
        path: paths[target] ?? paths.claude_code,
        created: false,
        backup: `${paths[target]}.nexterm.bak`,
        snippet: { mcpServers: { nexterm: { type: "http", url: "http://127.0.0.1:8799/mcp" } } },
      };
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
