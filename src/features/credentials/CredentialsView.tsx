// 凭据视图：把凭据库 + SSH 资产渲染成 ssh config 风格文本 / JSON。
//
// 动机很具体：以前"看一眼自己有哪些主机、用的什么密钥"要开 VSCode 去翻
// `~/.ssh/config`。这里给一个等价物，但它读的是**NexTerm 自己的库** ——
// 于是它同时也是"库长什么样"的答案。
//
// 三条边界（有意为之）：
// · 只读：没有写回，改东西回左栏与详情页，避免出现"文本是真相还是库是真相"；
// · 不含明文：私钥/密码正文一律不出现，只标注来源（要看明文去详情页，那里有 15 秒打回）；
// · 纯前端拼装：数据都来自已有的两个查询，不加内核命令 —— 这类展示不值得多一条 IPC。
import { useMemo, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { assetApi, vaultApi, type Asset, type Credential } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { kindMeta, formatTime } from "./meta";
import { ExportAssetsModal } from "./ExportAssetsModal";
import { IconCode, IconCopy, IconDownload, IconFile, IconRefresh } from "../../ui/icons";

type ViewMode = "text" | "json";

/** 只有真正走 SSH 的资产才配出现在 ssh config 里（docker 主机也是 SSH 连的）。 */
const SSH_KINDS = new Set(["ssh", "docker"]);

export function CredentialsView({
  view,
  onChange,
}: {
  /** 受控：形态存在标签上（`tab.credView`），左栏点「文本 / JSON」也能切过来。
   *  用组件内部 state 的话，标签已经开着时再点入口会"没反应"。 */
  view: ViewMode;
  onChange: (v: ViewMode) => void;
}) {
  const { pushToast } = useUi();
  const qc = useQueryClient();
  /** 导出弹窗（多选 + 含明文密码）——与只读文本视图分开，见 ExportAssetsModal 注释。 */
  const [exporting, setExporting] = useState(false);

  const assets = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), refetchOnWindowFocus: false });
  const creds = useQuery({ queryKey: ["credentials"], queryFn: () => vaultApi.listCredentials() });

  const list = assets.data ?? [];
  const credentials = creds.data ?? [];

  const text = useMemo(() => buildText(list, credentials), [list, credentials]);
  const json = useMemo(() => buildJson(list, credentials), [list, credentials]);
  const shown = view === "text" ? text : json;

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(shown);
      pushToast("success", view === "text" ? "已复制文本" : "已复制 JSON");
    } catch (e) {
      pushToast("error", `复制失败：${e instanceof Error ? e.message : String(e)}`);
    }
  };

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["assets"] });
    void qc.invalidateQueries({ queryKey: ["credentials"] });
  };

  return (
    <div className="flex h-full flex-col bg-neutral-900">
      <div className="flex h-[38px] shrink-0 items-center gap-2 border-b border-neutral-800/60 px-3">
        <IconCode size={14} className="text-neutral-400" />
        <span className="text-[13px] font-semibold text-neutral-100">凭据视图</span>
        <span className="nx-badge">只读</span>
        <div className="nx-segment ml-1">
          <button
            className={`nx-segment-item ${view === "text" ? "is-active" : ""}`}
            onClick={() => onChange("text")}
          >
            <IconFile size={12} />
            文本
          </button>
          <button
            className={`nx-segment-item ${view === "json" ? "is-active" : ""}`}
            onClick={() => onChange("json")}
          >
            <IconCode size={12} />
            JSON
          </button>
        </div>
        <div className="nx-spacer" />
        <span className="nx-hint hidden sm:inline">
          {credentials.length} 条凭据 · {list.filter((a) => SSH_KINDS.has(a.kind)).length} 台可 SSH 主机
        </span>
        <button className="nx-btn nx-btn-sm" title="重新读取" onClick={refresh}>
          <IconRefresh size={12} />
        </button>
        <button
          className="nx-btn nx-btn-sm"
          title="多选资产，导出为含明文密码的一行一条文本"
          onClick={() => setExporting(true)}
        >
          <IconDownload size={12} />
          导出到剪贴板
        </button>
        <button className="nx-btn nx-btn-sm" onClick={() => void copy()}>
          <IconCopy size={12} />
          复制
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto bg-neutral-950/40">
        <pre className="nx-mono w-full whitespace-pre px-5 py-4 text-[12px] leading-[1.7]">
          {view === "text" ? (
            <ConfigLines text={text} />
          ) : (
            json.split("\n").map((line, i) => <JsonLine key={i} line={line} />)
          )}
        </pre>
      </div>

      <div className="shrink-0 border-t border-neutral-800/60 px-3 py-1.5 text-[11px] text-neutral-600">
        私钥与密码正文保存在凭据库中，这里只呈现结构与来源；需要明文请在凭据详情里点「显示」，
        或经「导出到剪贴板」在选中资产上一次性取用。
      </div>

      {exporting && (
        <ExportAssetsModal
          assets={list}
          credentials={credentials}
          onClose={() => setExporting(false)}
        />
      )}
    </div>
  );
}

/* ── 文本 ─────────────────────────────────────────────────────────────── */

/**
 * ssh config 风格。刻意**不用** CodeMirror：这里只需要"注释灰、指令蓝、值亮"
 * 三档，自己渲染比挂一个编辑器实例更轻、也更容易控住行距。
 */
function buildText(assets: Asset[], creds: Credential[]): string {
  const now = formatTime(Date.now());
  const byId = new Map(creds.map((c) => [c.id, c]));
  const hosts = assets.filter((a) => SSH_KINDS.has(a.kind));
  const others = assets.filter((a) => !SSH_KINDS.has(a.kind));

  const out: string[] = [
    "# NexTerm 凭据视图 · 只读快照",
    `# 生成于 ${now}`,
    "#",
    "# 这份文本等价于「看一眼 ~/.ssh/config」，但它描述的是 NexTerm 资产库：",
    "# 私钥与密码正文保存在凭据库（可受密码保护），此处只标注来源。",
    `# 共 ${assets.length} 个资产 / ${creds.length} 条凭据。`,
    "",
  ];

  if (hosts.length === 0) {
    out.push("# 还没有可 SSH 的资产 —— 在左侧资产树里新建一台，或双击「当前设备」。");
    out.push("");
  }

  for (const a of hosts) {
    out.push(`Host ${a.name}`);
    if (a.host) out.push(`    HostName ${a.host}`);
    if (a.username) out.push(`    User ${a.username}`);
    if (a.port) out.push(`    Port ${a.port}`);
    if (a.kind === "docker") out.push("    # 类型：Docker 主机（按 SSH 连接）");
    out.push(`    ${authComment(a, byId)}`);
    if (a.tags) out.push(`    # 标签：${a.tags}`);
    if (a.note) out.push(`    # 备注：${a.note.replace(/\n/g, " ")}`);
    out.push("");
  }

  if (others.length > 0) {
    out.push("# 以下资产不走 SSH，没有对应的 ssh config 条目：");
    for (const a of others) {
      const kind = a.builtin ? `${a.kind}（内置）` : a.kind;
      out.push(`#   ${a.name}  [${kind}]${a.host ? `  ${a.host}${a.port ? `:${a.port}` : ""}` : ""}`);
    }
    out.push("");
  }

  out.push("# ── 凭据清单 ──────────────────────────────────────────");
  for (const c of creds) {
    const meta = kindMeta(c.kind);
    const used =
      c.usedBy.length > 0 ? `被 ${c.usedBy.length} 个资产使用：${c.usedBy.map((u) => u.name).join("、")}` : "未被引用";
    out.push(`# ${c.name}  [${meta.label}]  ${used}`);
  }
  return out.join("\n");
}

/** 认证方式那行注释：只说"钥匙在哪"，不吐正文。 */
function authComment(a: Asset, byId: Map<string, Credential>): string {
  const cred = a.credId ? byId.get(a.credId) : undefined;
  switch (a.authKind) {
    case "key":
      if (a.keyPath) {
        return `# IdentityFile ← 本地文件：${a.keyPath}${cred ? `（口令取自凭据「${cred.name}」）` : ""}`;
      }
      return cred
        ? `# IdentityFile ← 凭据库：${cred.name}（${kindMeta(cred.kind).label}）`
        : "# IdentityFile ← 未绑定私钥，连接时会报错";
    case "password":
      return cred ? `# 密码 ← 凭据库：${cred.name}` : "# 密码 ← 未绑定凭据";
    case "agent":
      return "# 认证 ← SSH Agent";
    case "ntlm":
      return cred ? `# NTLM ← 凭据库：${cred.name}` : "# 认证 ← WinRM / NTLM";
    default:
      return "# 认证方式未设置";
  }
}

/** 注释行灰、指令名蓝、值亮。 */
function ConfigLines({ text }: { text: string }) {
  const rows: ReactNode[] = [];
  text.split("\n").forEach((line, i) => {
    const trimmed = line.trimStart();
    if (trimmed.startsWith("#")) {
      rows.push(
        <div key={i} className="text-neutral-600">
          {line}
        </div>,
      );
      return;
    }
    const m = /^(\s*)(\S+)(\s+)(.*)$/.exec(line);
    if (!m) {
      rows.push(<div key={i}>{line}</div>);
      return;
    }
    rows.push(
      <div key={i}>
        <span>{m[1]}</span>
        <span className="text-blue-300">{m[2]}</span>
        <span>{m[3]}</span>
        <span className="text-neutral-200">{m[4]}</span>
      </div>,
    );
  });
  return <>{rows}</>;
}

/* ── JSON ─────────────────────────────────────────────────────────────── */

function buildJson(assets: Asset[], creds: Credential[]): string {
  const byId = new Map(creds.map((c) => [c.id, c]));
  const payload = {
    source: "nexterm",
    generatedAt: formatTime(Date.now()),
    counts: {
      assets: assets.length,
      credentials: creds.length,
      sshHosts: assets.filter((a) => SSH_KINDS.has(a.kind)).length,
    },
    hosts: assets
      .filter((a) => SSH_KINDS.has(a.kind))
      .map((a) => {
        const cred = a.credId ? byId.get(a.credId) : undefined;
        return {
          alias: a.name,
          host: a.host,
          port: a.port,
          user: a.username,
          kind: a.kind,
          builtin: a.builtin,
          auth: {
            kind: a.authKind,
            keyPath: a.keyPath,
            credential: cred ? { id: cred.id, name: cred.name, kind: cred.kind } : null,
          },
          tags: a.tags || null,
          note: a.note || null,
        };
      }),
    credentials: creds.map((c) => ({
      id: c.id,
      name: c.name,
      kind: c.kind,
      usedBy: c.usedBy.map((u) => u.name),
      createdAt: formatTime(c.createdAt),
      updatedAt: formatTime(c.updatedAt),
    })),
    otherAssets: assets
      .filter((a) => !SSH_KINDS.has(a.kind))
      .map((a) => ({ name: a.name, kind: a.kind, builtin: a.builtin ?? false })),
  };
  return JSON.stringify(payload, null, 2);
}

const JSON_TOKEN = /("(?:\\.|[^"\\])*")(\s*:)?|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|\b(true|false|null)\b/g;

/** 单行 JSON 上色：键蓝、字符串值绿、数字琥珀、布尔与 null 紫。 */
function JsonLine({ line }: { line: string }) {
  const parts: ReactNode[] = [];
  let last = 0;
  let m: RegExpExecArray | null;
  JSON_TOKEN.lastIndex = 0;
  while ((m = JSON_TOKEN.exec(line)) !== null) {
    if (m.index > last) parts.push(line.slice(last, m.index));
    if (m[1] !== undefined) {
      // 后面跟着冒号的字符串是键，否则是值
      parts.push(
        <span key={`${m.index}-s`} className={m[2] ? "text-blue-300" : "text-green-300"}>
          {m[1]}
        </span>,
      );
      if (m[2]) parts.push(<span key={`${m.index}-c`} className="text-neutral-500">{m[2]}</span>);
    } else if (m[3] !== undefined) {
      parts.push(
        <span key={`${m.index}-n`} className="text-amber-300">
          {m[3]}
        </span>,
      );
    } else {
      parts.push(
        <span key={`${m.index}-k`} className="text-purple-300">
          {m[0]}
        </span>,
      );
    }
    last = m.index + m[0].length;
  }
  if (last < line.length) parts.push(line.slice(last));
  return <div>{parts}</div>;
}
