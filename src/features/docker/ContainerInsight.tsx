// 容器洞察钻取视图（M61）：inspect 详情 / 可轮询 stats / 容器内目录浏览。
//
// 三条视图全部只走既有 `dockerApi` facade（docker_inspect / docker_stats /
// docker_container_list_dir），sessionId 一律来自标签上下文 —— 用户没有任何输入
// session 的入口，缓存键也全部带 sessionId，跨会话不会互相读到对方的缓存。
//
// 脱敏口径：env / label / mount / 文件名里的敏感值默认**不在界面明文渲染**
// （见 dockerRedact.ts）。这只是展示级遮蔽 —— 所有权校验与强制遮蔽以内核为准，
// 界面上的遮蔽开关不构成授权，也不替代内核检查。

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { dockerApi, type ContainerSummary } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import {
  IconArrowLeft,
  IconArrowUp,
  IconBox,
  IconFile,
  IconFolder,
  IconMonitor,
  IconRefresh,
  IconShield,
  IconTable,
} from "../../ui/icons";
import {
  isSensitiveFileName,
  isSensitiveName,
  pickInspectNumber,
  pickInspectString,
  redactInspectTree,
  REDACTED_MARK,
  splitEnvEntry,
} from "./dockerRedact";
import { parseStatsOutput } from "./statsParse";

interface InsightProps {
  sessionId: string;
  container: ContainerSummary;
  /** 面板是否在前台（标签不可见时停掉一切轮询）。 */
  visible: boolean;
  onClose: () => void;
}

type InsightTab = "inspect" | "stats" | "files";

export function ContainerInsight({ sessionId, container, visible, onClose }: InsightProps) {
  const [tab, setTab] = useState<InsightTab>("inspect");

  return (
    <div className="nx-pane">
      <div className="nx-toolbar">
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={onClose}>
          <IconArrowLeft size={13} />
          返回
        </button>
        <span className="nx-toolbar-title">{container.name}</span>
        <span className={`nx-badge ${container.state === "running" ? "nx-badge-green" : "nx-badge-red"}`}>
          <span className="nx-dot" />
          {container.state}
        </span>
        <div className="nx-spacer" />
        <div className="nx-segment">
          <button
            className={`nx-segment-item ${tab === "inspect" ? "is-active" : ""}`}
            onClick={() => setTab("inspect")}
          >
            <IconMonitor size={12} />
            详情
          </button>
          <button
            className={`nx-segment-item ${tab === "stats" ? "is-active" : ""}`}
            onClick={() => setTab("stats")}
          >
            <IconTable size={12} />
            统计
          </button>
          <button
            className={`nx-segment-item ${tab === "files" ? "is-active" : ""}`}
            onClick={() => setTab("files")}
          >
            <IconFolder size={12} />
            文件
          </button>
        </div>
      </div>

      {/* 切换容器时 DockerPanel 用 key={container.id} 重建本组件，任何在途响应
          都只能落进旧键的缓存，不会渲染到新容器头上。 */}
      {tab === "inspect" && <InspectView sessionId={sessionId} containerId={container.id} />}
      {tab === "stats" && (
        <StatsView
          sessionId={sessionId}
          containerId={container.id}
          containerRunning={container.state === "running"}
          active={visible}
        />
      )}
      {tab === "files" && <FilesView sessionId={sessionId} containerId={container.id} />}
    </div>
  );
}

function InsightLoading({ text }: { text: string }) {
  return <div className="nx-empty">{text}</div>;
}

function InsightError({
  error,
  onRetry,
  retryText = "重试",
  extra,
}: {
  error: unknown;
  onRetry: () => void;
  retryText?: string;
  extra?: React.ReactNode;
}) {
  return (
    <div className="nx-alert nx-alert-danger m-3 flex items-start gap-2">
      <span className="min-w-0 flex-1">{describeError(error)}</span>
      <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={onRetry}>
        <IconRefresh size={12} />
        {retryText}
      </button>
      {extra}
    </div>
  );
}

/** 展示级脱敏的统一口径提示。 */
function RedactHint({ revealed }: { revealed: boolean }) {
  return (
    <span className="nx-hint inline-flex items-center gap-1">
      <IconShield size={11} />
      {revealed
        ? "敏感值正在明文显示 · 遮蔽只是展示层，所有权与强制遮蔽以内核为准"
        : "敏感值已遮蔽 · 展示级脱敏，所有权与强制遮蔽以内核为准"}
    </span>
  );
}

// ───────── 详情（inspect） ─────────

function InspectView({ sessionId, containerId }: { sessionId: string; containerId: string }) {
  const [reveal, setReveal] = useState(false);
  const q = useQuery({
    queryKey: ["docker-inspect", sessionId, containerId],
    queryFn: () => dockerApi.inspect(sessionId, containerId),
  });

  if (q.isPending) return <InsightLoading text="读取容器配置…" />;
  if (q.isError) {
    return <InsightError error={q.error} onRetry={() => void q.refetch()} />;
  }

  const raw = q.data;
  // docker CLI 的 inspect 输出是单元素数组，moby SDK 返回单个对象，两种都接。
  const root = Array.isArray(raw) ? raw[0] : raw;
  const redacted = redactInspectTree(raw, reveal);

  const name = pickInspectString(root, "Name").replace(/^\//, "");
  const image = pickInspectString(root, "Config.Image");
  const state = pickInspectString(root, "State.Status");
  const startedAt = pickInspectString(root, "State.StartedAt");
  const restartCount = pickInspectNumber(root, "State.RestartCount");

  const envEntries = readStringArray(root, "Config.Env");
  const labels = readStringRecord(root, "Config.Labels");
  const mounts = readObjectArray(root, "Mounts").map((m) => {
    const rawName = pickInspectString(m, "Name");
    return {
      type: pickInspectString(m, "Type"),
      // 卷名本身可能带敏感词（如 db-password-token）；`Name` 这个键名不敏感，
      // 键值规则盖不到它，所以在这里按值显式判一次。主机路径保留 ——
      // 排查挂载问题必须看到源路径。
      name: !reveal && rawName && isSensitiveName(rawName) ? REDACTED_MARK : rawName,
      source: pickInspectString(m, "Source"),
      destination: pickInspectString(m, "Destination"),
      rw: pickInspectString(m, "RW") || "false",
    };
  });

  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <div className="flex items-center gap-2 border-b border-neutral-800/60 px-3 py-2">
        <button
          className="nx-btn nx-btn-ghost nx-btn-sm"
          onClick={() => setReveal((v) => !v)}
          aria-pressed={reveal}
        >
          <IconShield size={12} />
          {reveal ? "遮蔽敏感值" : "显示敏感值"}
        </button>
        <RedactHint revealed={reveal} />
        <div className="nx-spacer" />
        {q.isFetching && <span className="nx-hint">刷新中…</span>}
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void q.refetch()}>
          <IconRefresh size={12} />
          刷新
        </button>
      </div>

      <div className="grid grid-cols-2 gap-x-6 gap-y-1.5 px-3 py-3 md:grid-cols-3">
        <Field label="名称" value={name} />
        <Field label="镜像" value={image} mono />
        <Field label="状态" value={state} />
        <Field label="启动时间" value={startedAt} mono />
        <Field label="重启次数" value={restartCount} />
        <Field label="ID" value={containerId} mono />
      </div>

      <Section title={`环境变量 (${envEntries.length})`}>
        {envEntries.length === 0 ? (
          <span className="nx-hint">没有环境变量</span>
        ) : (
          <table className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th style={{ width: 240 }}>键</th>
                <th>值</th>
              </tr>
            </thead>
            <tbody>
              {envEntries.map((entry) => {
                const { name: k, value } = splitEnvEntry(entry);
                const hidden = !reveal && isSensitiveName(k);
                return (
                  <tr key={entry}>
                    <td className="nx-mono truncate text-neutral-200" title={k}>
                      {k}
                    </td>
                    <td className="nx-mono truncate text-neutral-400" title={hidden ? "已遮蔽" : value}>
                      {hidden ? REDACTED_MARK : value || "—"}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </Section>

      <Section title={`标签 (${Object.keys(labels).length})`}>
        {Object.keys(labels).length === 0 ? (
          <span className="nx-hint">没有标签</span>
        ) : (
          <table className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th style={{ width: 280 }}>键</th>
                <th>值</th>
              </tr>
            </thead>
            <tbody>
              {Object.entries(labels).map(([k, v]) => {
                const hidden = !reveal && isSensitiveName(k);
                return (
                  <tr key={k}>
                    <td className="nx-mono truncate text-neutral-200" title={k}>
                      {k}
                    </td>
                    <td className="nx-mono truncate text-neutral-400" title={hidden ? "已遮蔽" : v}>
                      {hidden ? REDACTED_MARK : v || "—"}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </Section>

      <Section title={`挂载 (${mounts.length})`}>
        {mounts.length === 0 ? (
          <span className="nx-hint">没有挂载</span>
        ) : (
          <table className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th style={{ width: 80 }}>类型</th>
                <th style={{ width: 180 }}>名称</th>
                <th>源</th>
                <th>目标</th>
                <th style={{ width: 56 }}>读写</th>
              </tr>
            </thead>
            <tbody>
              {mounts.map((m, i) => (
                <tr key={`${m.source}-${m.destination}-${i}`}>
                  <td>{m.type || "—"}</td>
                  <td className="nx-mono truncate" title={m.name}>
                    {m.name || "—"}
                  </td>
                  <td className="nx-mono truncate" title={m.source}>
                    {m.source || "—"}
                  </td>
                  <td className="nx-mono truncate" title={m.destination}>
                    {m.destination || "—"}
                  </td>
                  <td>{m.rw === "true" ? "RW" : "RO"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section title="完整配置（已脱敏）">
        <pre className="nx-pre m-3 whitespace-pre-wrap break-all">
          {JSON.stringify(redacted, null, 2)}
        </pre>
      </Section>
    </div>
  );
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex min-w-0 items-baseline gap-2">
      <span className="shrink-0 text-[11px] text-neutral-500">{label}</span>
      <span className={`truncate text-[12px] text-neutral-200 ${mono ? "nx-mono" : ""}`} title={value}>
        {value || "—"}
      </span>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="border-t border-neutral-800/60 px-3 py-2.5">
      <div className="mb-1.5 text-[11px] font-medium text-neutral-400">{title}</div>
      {children}
    </div>
  );
}

function readStringArray(root: unknown, path: string): string[] {
  let node: unknown = root;
  for (const part of path.split(".")) {
    if (!node || typeof node !== "object") return [];
    node = (node as Record<string, unknown>)[part];
  }
  return Array.isArray(node) ? node.filter((v): v is string => typeof v === "string") : [];
}

function readStringRecord(root: unknown, path: string): Record<string, string> {
  let node: unknown = root;
  for (const part of path.split(".")) {
    if (!node || typeof node !== "object") return {};
    node = (node as Record<string, unknown>)[part];
  }
  if (!node || typeof node !== "object" || Array.isArray(node)) return {};
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(node as Record<string, unknown>)) {
    if (typeof v === "string") out[k] = v;
  }
  return out;
}

function readObjectArray(root: unknown, path: string): Record<string, unknown>[] {
  let node: unknown = root;
  for (const part of path.split(".")) {
    if (!node || typeof node !== "object") return [];
    node = (node as Record<string, unknown>)[part];
  }
  if (!Array.isArray(node)) return [];
  return node.filter(
    (v): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v),
  );
}

// ───────── 统计（stats） ─────────

function StatsView({
  sessionId,
  containerId,
  containerRunning,
  active,
}: {
  sessionId: string;
  containerId: string;
  containerRunning: boolean;
  active: boolean;
}) {
  // stats 是**会话级**快照（一次返回全部运行中容器），live 感靠有界轮询：
  // 只在「统计页签在前台 && 面板可见」时 3s 一跳；切页签 / 切标签 / 卸载即停。
  const stats = useQuery({
    queryKey: ["docker-stats", sessionId],
    queryFn: () => dockerApi.stats(sessionId),
    refetchInterval: active ? 3000 : false,
  });
  // 展示级归属比对：只渲染当前会话容器清单里有的行，后端多返回/缓存串了的行
  // 不上屏。清单还没加载出来时不拦 —— 后端本身就是按会话隔离的权威边界。
  const ps = useQuery({
    queryKey: ["docker-ps", sessionId],
    queryFn: () => dockerApi.ps(sessionId),
  });

  if (stats.isPending) return <InsightLoading text="读取容器统计…" />;
  if (stats.isError) {
    return <InsightError error={stats.error} onRetry={() => void stats.refetch()} />;
  }

  const known = new Set((ps.data ?? []).map((c) => c.id));
  const parsed = parseStatsOutput(stats.data ?? "");
  const rows = ps.isSuccess ? parsed.filter((r) => known.has(r.id)) : parsed;
  const current = rows.find((r) => r.id === containerId);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-2 border-b border-neutral-800/60 px-3 py-2">
        <span className="nx-hint">
          3s 轮询 · 切走即停 · 共 {rows.length} 个运行中容器
        </span>
        {!containerRunning && <span className="nx-badge nx-badge-amber">当前容器已停止</span>}
        <div className="nx-spacer" />
        {stats.isFetching && <span className="nx-hint">刷新中…</span>}
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void stats.refetch()}>
          <IconRefresh size={12} />
          刷新
        </button>
      </div>

      {rows.length === 0 ? (
        <div className="nx-empty">
          {containerRunning ? "暂时没有统计行返回" : "当前容器已停止，只有运行中的容器才有统计"}
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto">
          <table className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th style={{ width: 220 }}>容器</th>
                <th style={{ width: 96 }}>CPU %</th>
                <th style={{ width: 150 }}>内存用量</th>
                <th style={{ width: 96 }}>内存 %</th>
                <th style={{ width: 170 }}>网络 IO</th>
                <th style={{ width: 170 }}>块 IO</th>
                <th style={{ width: 70 }}>PIDs</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const isCurrent = r.id === containerId;
                return (
                  <tr key={r.id} className={isCurrent ? "font-medium text-neutral-100" : undefined}>
                    <td className="truncate" title={r.name}>
                      {isCurrent && <IconBox size={12} className="mr-1.5 inline align-[-2px] text-green-400" />}
                      {r.name || r.id}
                      {isCurrent && <span className="nx-badge nx-badge-green ml-1.5">当前</span>}
                    </td>
                    <td className="nx-mono">{r.cpuPerc || "—"}</td>
                    <td className="nx-mono truncate" title={r.memUsage}>
                      {r.memUsage || "—"}
                    </td>
                    <td className="nx-mono">{r.memPerc || "—"}</td>
                    <td className="nx-mono truncate" title={r.netIO}>
                      {r.netIO || "—"}
                    </td>
                    <td className="nx-mono truncate" title={r.blockIO}>
                      {r.blockIO || "—"}
                    </td>
                    <td className="nx-mono">{r.pids || "—"}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {current && (
            <div className="nx-hint px-3 py-2">
              当前容器 {current.name || containerId} · CPU {current.cpuPerc || "—"} · 内存{" "}
              {current.memUsage || "—"}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ───────── 文件（容器内目录） ─────────

/** `ls -1a` 的输出带 `.` / `..`，后端原样透传，这里必须滤掉。 */
function cleanEntries(entries: string[]): string[] {
  return entries
    .map((e) => e.trim())
    .filter((e) => e !== "" && e !== "." && e !== "..");
}

function joinContainerPath(parent: string, name: string): string {
  const clean = name.replace(/\/+$/, "");
  return parent === "/" ? `/${clean}` : `${parent}/${clean}`;
}

function parentOfContainerPath(path: string): string | null {
  if (path === "/" || path === "") return null;
  const idx = path.lastIndexOf("/");
  return idx <= 0 ? "/" : path.slice(0, idx);
}

function crumbsOf(path: string): { label: string; path: string }[] {
  const crumbs = [{ label: "/", path: "/" }];
  let acc = "";
  for (const seg of path.split("/").filter(Boolean)) {
    acc += `/${seg}`;
    crumbs.push({ label: seg, path: acc });
  }
  return crumbs;
}

function FilesView({ sessionId, containerId }: { sessionId: string; containerId: string }) {
  const [path, setPath] = useState("/");
  const q = useQuery({
    queryKey: ["docker-container-files", sessionId, containerId, path],
    queryFn: () => dockerApi.containerListDir(sessionId, containerId, path),
    // 路径打错/无权访问时自动重试没有意义，重试交给用户点的按钮。
    retry: false,
  });

  const up = parentOfContainerPath(path);
  const entries = cleanEntries(q.data ?? []);
  // 带 `/` 后缀的条目（demo 数据里的目录）排前面；裸 `ls -1a` 输出没有类型信息，
  // 排序只影响观感，能不能进由后端说了算。
  const sorted = [...entries].sort((a, b) => {
    const da = a.endsWith("/") ? 0 : 1;
    const db = b.endsWith("/") ? 0 : 1;
    return da !== db ? da - db : a.localeCompare(b);
  });

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="nx-pathbar">
        <button
          className="nx-tree-caret"
          title={up ? `上级：${up}` : "已经在根目录"}
          disabled={!up || q.isFetching}
          onClick={() => up && setPath(up)}
        >
          <IconArrowUp size={12} />
        </button>
        <div className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto">
          {crumbsOf(path).map((c, i, all) => (
            <span key={c.path} className="flex shrink-0 items-center gap-0.5">
              {i > 0 && all[i - 1].label !== "/" && <span className="text-neutral-600">/</span>}
              <button
                className={`nx-path-crumb ${i === all.length - 1 ? "is-current" : ""}`}
                onClick={() => setPath(c.path)}
                title={c.path}
              >
                {c.label}
              </button>
            </span>
          ))}
        </div>
        <button
          className="nx-tree-caret"
          title="刷新"
          disabled={q.isFetching}
          onClick={() => void q.refetch()}
        >
          <IconRefresh size={12} />
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {q.isPending ? (
          <InsightLoading text="读取容器目录…" />
        ) : q.isError ? (
          <InsightError
            error={q.error}
            onRetry={() => void q.refetch()}
            extra={
              up ? (
                <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setPath(up)}>
                  返回上级
                </button>
              ) : undefined
            }
          />
        ) : sorted.length === 0 ? (
          <div className="nx-empty">这个目录是空的</div>
        ) : (
          <table className="nx-table nx-table-fixed">
            <tbody>
              {sorted.map((entry) => {
                const isDir = entry.endsWith("/");
                const name = entry.replace(/\/+$/, "");
                const sensitive = isSensitiveFileName(name);
                // Go 后端是裸 `ls -1a` 输出（目录名不带 `/` 后缀），前端无法可靠区分
                // 文件与目录 —— 所有条目都可点，当作目录进入；点到文件时后端会报错，
                // 原地给重试 / 返回上级（见上面的错误分支）。
                return (
                  <tr
                    key={entry}
                    className="cursor-pointer"
                    title="作为目录进入"
                    onClick={() => setPath(joinContainerPath(path, name))}
                  >
                    <td className="truncate text-neutral-200" title={joinContainerPath(path, name)}>
                      {isDir ? (
                        <IconFolder size={13} className="mr-1.5 inline align-[-2px] text-neutral-500" />
                      ) : (
                        <IconFile size={13} className="mr-1.5 inline align-[-2px] text-neutral-600" />
                      )}
                      {name}
                      {sensitive && (
                        <span className="nx-badge nx-badge-amber ml-1.5" title="疑似敏感文件：仅列出名称，不提供内容读取">
                          敏感
                        </span>
                      )}
                    </td>
                    <td style={{ width: 130 }} className="nx-hint nx-right">
                      {isDir ? "目录" : ""}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      <div className="shrink-0 border-t border-neutral-800/60 px-3 py-1.5">
        <span className="nx-hint">仅列出容器内目录，不读取文件内容</span>
      </div>
    </div>
  );
}
