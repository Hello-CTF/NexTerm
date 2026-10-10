
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { dockerApi, type ContainerSummary } from "../../ipc/commands";
import { describeDockerError } from "./dockerErrors";
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
  buildInspectViewModel,
  isSensitiveFileName,
  redactContainerPath,
  redactFileName,
  redactPathInText,
} from "./dockerRedact";
import { parseStatsOutput } from "./statsParse";

interface InsightProps {
  sessionId: string;
  container: ContainerSummary;
  visible: boolean;
  onClose: () => void;
}

type InsightTab = "inspect" | "stats" | "files";

export function ContainerInsight({ sessionId, container, visible, onClose }: InsightProps) {
  const [tab, setTab] = useState<InsightTab>("inspect");
  const running = container.state === "running";

  return (
    <div className="nx-pane">
      <div className="nx-toolbar flex-wrap">
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={onClose}>
          <IconArrowLeft size={13} />
          返回
        </button>
        <span className="nx-toolbar-title">{container.name}</span>
        <span className={`nx-badge ${running ? "nx-badge-green" : "nx-badge-red"}`}>
          <span className="nx-dot" />
          {running ? "运行中" : container.state === "exited" ? "已停止" : container.state}
        </span>
        {!running && <span className="nx-hint">启动容器后可浏览文件</span>}
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
            disabled={!running}
            title={running ? "浏览容器文件" : "容器已停止，启动后可浏览文件"}
          >
            <IconFolder size={12} />
            文件
          </button>
        </div>
      </div>

      {tab === "inspect" && <InspectView sessionId={sessionId} containerId={container.id} />}
      {tab === "stats" && (
        <StatsView
          sessionId={sessionId}
          containerId={container.id}
          containerRunning={running}
          active={visible}
        />
      )}
      {tab === "files" &&
        (running ? (
          <FilesView sessionId={sessionId} containerId={container.id} />
        ) : (
          <div className="nx-empty">容器已停止，启动后可浏览文件</div>
        ))}
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
      <span className="min-w-0 flex-1">{describeDockerError(error)}</span>
      <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={onRetry}>
        <IconRefresh size={12} />
        {retryText}
      </button>
      {extra}
    </div>
  );
}

function RedactHint() {
  return (
    <span className="nx-hint inline-flex min-w-0 items-center gap-1">
      <IconShield size={11} />
      敏感值已遮蔽 · 按名称和连接串内嵌凭据自动识别
    </span>
  );
}

function InspectView({ sessionId, containerId }: { sessionId: string; containerId: string }) {
  const q = useQuery({
    queryKey: ["docker-inspect", sessionId, containerId],
    queryFn: () => dockerApi.inspect(sessionId, containerId),
  });

  if (q.isPending) return <InsightLoading text="读取容器配置…" />;
  if (q.isError) {
    return <InsightError error={q.error} onRetry={() => void q.refetch()} />;
  }

  const vm = buildInspectViewModel(q.data);

  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <div className="flex flex-wrap items-center gap-2 border-b border-neutral-800/60 px-3 py-2">
        <RedactHint />
        <div className="nx-spacer" />
        {q.isFetching && <span className="nx-hint">刷新中…</span>}
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void q.refetch()}>
          <IconRefresh size={12} />
          刷新
        </button>
      </div>

      <div className="grid grid-cols-2 gap-x-6 gap-y-1.5 px-3 py-3 md:grid-cols-3">
        <Field label="名称" value={vm.name} />
        <Field label="镜像" value={vm.image} mono />
        <Field label="状态" value={vm.state} />
        <Field label="启动时间" value={vm.startedAt} mono />
        <Field label="重启次数" value={vm.restartCount} />
        <Field label="ID" value={containerId} mono />
      </div>

      <Section title={`环境变量 (${vm.env.length})`}>
        {vm.env.length === 0 ? (
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
              {vm.env.map((row) => (
                <tr key={row.key}>
                  <td className="nx-mono truncate text-neutral-200" title={row.key}>
                    {row.key}
                  </td>
                  <td className="nx-mono truncate text-neutral-400" title={row.value}>
                    {row.value || "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section title={`标签 (${vm.labels.length})`}>
        {vm.labels.length === 0 ? (
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
              {vm.labels.map((row) => (
                <tr key={row.key}>
                  <td className="nx-mono truncate text-neutral-200" title={row.key}>
                    {row.key}
                  </td>
                  <td className="nx-mono truncate text-neutral-400" title={row.value}>
                    {row.value || "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section title={`挂载 (${vm.mounts.length})`}>
        {vm.mounts.length === 0 ? (
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
              {vm.mounts.map((m, i) => (
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
                  <td>{m.rw ? "RW" : "RO"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section title="完整配置（已脱敏）">
        <pre className="nx-pre m-3 whitespace-pre-wrap break-all">{vm.json}</pre>
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
  const stats = useQuery({
    queryKey: ["docker-stats", sessionId],
    queryFn: () => dockerApi.stats(sessionId),
    refetchInterval: active ? 3000 : false,
  });
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

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-neutral-800/60 px-3 py-2">
        <span className="nx-hint">
          每 3 秒自动刷新 · 切走即停 · 共 {rows.length} 个运行中容器
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
          {containerRunning ? "还没有统计输出（每 3 秒刷新）" : "当前容器已停止，只有运行中的容器才有统计"}
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto">
          <table className="nx-table nx-table-fixed min-w-[620px]">
            <thead>
              <tr>
                <th style={{ width: 160 }} className="left-0 z-[2] shadow-[inset_-1px_0_0_var(--nx-border)]">
                  容器
                </th>
                <th>CPU %</th>
                <th>内存用量</th>
                <th>内存 %</th>
                <th>网络 IO</th>
                <th>块 IO</th>
                <th>PIDs</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const isCurrent = r.id === containerId;
                return (
                  <tr key={r.id} className={isCurrent ? "font-medium text-neutral-100" : undefined}>
                    <td
                      className="truncate sticky left-0 bg-[var(--nx-bg-pane)] shadow-[inset_-1px_0_0_var(--nx-border)]"
                      title={r.name}
                    >
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
        </div>
      )}
    </div>
  );
}

interface ContainerDirEntry {
  name: string;
  isDir: boolean;
}

function cleanEntries(entries: readonly (string | ContainerDirEntry)[]): ContainerDirEntry[] {
  return entries
    .map((entry) => {
      if (typeof entry !== "string") {
        return { name: entry.name.trim(), isDir: entry.isDir };
      }
      const name = entry.trim();
      return { name: name.replace(/\/+$/, ""), isDir: name.endsWith("/") };
    })
    .filter((entry) => entry.name !== "" && entry.name !== "." && entry.name !== "..");
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

function crumbsOf(path: string): { label: string; path: string; sensitive: boolean }[] {
  const crumbs = [{ label: "/", path: "/", sensitive: false }];
  let acc = "";
  for (const seg of path.split("/").filter(Boolean)) {
    acc += `/${seg}`;
    const sensitive = isSensitiveFileName(seg);
    crumbs.push({ label: sensitive ? redactFileName(seg) : seg, path: acc, sensitive });
  }
  return crumbs;
}

function FilesView({ sessionId, containerId }: { sessionId: string; containerId: string }) {
  const [path, setPath] = useState("/");
  const q = useQuery({
    queryKey: ["docker-container-files", sessionId, containerId, path],
    queryFn: () => dockerApi.containerListDir(sessionId, containerId, path),
    retry: false,
  });

  const up = parentOfContainerPath(path);
  const entries = cleanEntries(q.data ?? []);
  const sorted = [...entries].sort((a, b) => {
    const da = a.isDir ? 0 : 1;
    const db = b.isDir ? 0 : 1;
    return da !== db ? da - db : a.name.localeCompare(b.name);
  });

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="nx-pathbar">
        <button
          className="nx-tree-caret"
          title={up ? `上级：${redactContainerPath(up)}` : "已经在根目录"}
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
                title={c.sensitive ? "已遮蔽" : redactContainerPath(c.path)}
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
            error={redactPathInText(describeDockerError(q.error), path)}
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
              {sorted.map(({ name, isDir }) => {
                const sensitive = isSensitiveFileName(name);
                const display = sensitive ? redactFileName(name) : name;
                return (
                  <tr
                    key={name}
                    className={isDir ? "cursor-pointer" : undefined}
                    title={
                      sensitive
                        ? "已遮蔽"
                        : isDir
                          ? redactContainerPath(joinContainerPath(path, name))
                          : `${redactContainerPath(joinContainerPath(path, name))}（文件，仅列出目录，不读取内容）`
                    }
                    onClick={isDir ? () => setPath(joinContainerPath(path, name)) : undefined}
                  >
                    <td className="truncate text-neutral-200">
                      {isDir ? (
                        <IconFolder size={13} className="mr-1.5 inline align-[-2px] text-neutral-500" />
                      ) : (
                        <IconFile size={13} className="mr-1.5 inline align-[-2px] text-neutral-600" />
                      )}
                      {display}
                      {sensitive && (
                        <span className="nx-badge nx-badge-amber ml-1.5" title="疑似敏感文件：名称已遮蔽，不提供内容读取">
                          敏感
                        </span>
                      )}
                    </td>
                    <td style={{ width: 130 }} className="nx-hint nx-right">
                      {isDir ? "目录" : "文件"}
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
