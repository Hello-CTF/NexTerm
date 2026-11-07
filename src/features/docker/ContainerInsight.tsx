
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
import { t } from "../../i18n";

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
        <button className="nx-btn nx-btn-ghost nx-btn-sm pointer-coarse:min-h-9" onClick={onClose}>
          <IconArrowLeft size={13} />
          {t("返回")}
        </button>
        <span className="nx-toolbar-title min-w-0 max-w-full truncate" title={container.name}>{container.name}</span>
        <span className={`nx-badge ${running ? "nx-badge-green" : "nx-badge-red"}`}>
          <span className="nx-dot" />
          {running ? t("运行中") : container.state === "exited" ? t("已停止") : container.state}
        </span>
        {!running && <span className="nx-hint">{t("启动容器后可浏览文件")}</span>}
        <div className="nx-spacer" />
        <div className="nx-segment">
          <button
            className={`nx-segment-item ${tab === "inspect" ? "is-active" : ""}`}
            onClick={() => setTab("inspect")}
          >
            <IconMonitor size={12} />
            {t("详情")}
          </button>
          <button
            className={`nx-segment-item ${tab === "stats" ? "is-active" : ""}`}
            onClick={() => setTab("stats")}
          >
            <IconTable size={12} />
            {t("统计")}
          </button>
          <button
            className={`nx-segment-item ${tab === "files" ? "is-active" : ""}`}
            onClick={() => setTab("files")}
            disabled={!running}
            title={running ? t("浏览容器文件") : t("容器已停止，启动后可浏览文件")}
          >
            <IconFolder size={12} />
            {t("文件")}
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
          <div className="nx-empty">{t("容器已停止，启动后可浏览文件")}</div>
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
  retryText = t("重试"),
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
      {t("已根据变量名和连接字符串中的凭据自动脱敏敏感值")}
    </span>
  );
}

function InspectView({ sessionId, containerId }: { sessionId: string; containerId: string }) {
  const q = useQuery({
    queryKey: ["docker-inspect", sessionId, containerId],
    queryFn: () => dockerApi.inspect(sessionId, containerId),
  });

  if (q.isPending) return <InsightLoading text={t("读取容器配置…")} />;
  if (q.isError) {
    return <InsightError error={q.error} onRetry={() => void q.refetch()} />;
  }

  const vm = buildInspectViewModel(q.data);

  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <div className="flex flex-wrap items-center gap-2 border-b border-neutral-800/60 px-3 py-2">
        <RedactHint />
        <div className="nx-spacer" />
        {q.isFetching && <span className="nx-hint">{t("正在刷新…")}</span>}
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void q.refetch()}>
          <IconRefresh size={12} />
          {t("刷新")}
        </button>
      </div>

      <div className="grid grid-cols-2 gap-x-6 gap-y-1.5 px-3 py-3 md:grid-cols-3">
        <Field label={t("名称")} value={vm.name} />
        <Field label={t("镜像")} value={vm.image} mono />
        <Field label={t("状态")} value={vm.state} />
        <Field label={t("启动时间")} value={vm.startedAt} mono />
        <Field label={t("重启次数")} value={vm.restartCount} />
        <Field label="ID" value={containerId} mono />
      </div>

      <Section title={t("环境变量（{length}）", { length: vm.env.length })}>
        {vm.env.length === 0 ? (
          <span className="nx-hint">{t("没有环境变量")}</span>
        ) : (
          <table className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th style={{ width: 240 }}>{t("键")}</th>
                <th>{t("值")}</th>
              </tr>
            </thead>
            <tbody>
              {vm.env.map((row) => (
                <tr key={row.key}>
                  <td className="nx-mono truncate max-[560px]:overflow-visible max-[560px]:break-all max-[560px]:whitespace-normal text-neutral-200" title={row.key}>
                    {row.key}
                  </td>
                  <td className="nx-mono truncate max-[560px]:overflow-visible max-[560px]:break-all max-[560px]:whitespace-normal text-neutral-400" title={row.value}>
                    {row.value || "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section title={t("标签（{length}）", { length: vm.labels.length })}>
        {vm.labels.length === 0 ? (
          <span className="nx-hint">{t("没有标签")}</span>
        ) : (
          <table className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th style={{ width: 280 }}>{t("键")}</th>
                <th>{t("值")}</th>
              </tr>
            </thead>
            <tbody>
              {vm.labels.map((row) => (
                <tr key={row.key}>
                  <td className="nx-mono truncate max-[560px]:overflow-visible max-[560px]:break-all max-[560px]:whitespace-normal text-neutral-200" title={row.key}>
                    {row.key}
                  </td>
                  <td className="nx-mono truncate max-[560px]:overflow-visible max-[560px]:break-all max-[560px]:whitespace-normal text-neutral-400" title={row.value}>
                    {row.value || "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section title={t("挂载（{length}）", { length: vm.mounts.length })}>
        {vm.mounts.length === 0 ? (
          <span className="nx-hint">{t("没有挂载")}</span>
        ) : (
          <table className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th style={{ width: 80 }}>{t("类型")}</th>
                <th style={{ width: 180 }}>{t("名称")}</th>
                <th>{t("源")}</th>
                <th>{t("目标")}</th>
                <th style={{ width: 56 }}>{t("读写")}</th>
              </tr>
            </thead>
            <tbody>
              {vm.mounts.map((m, i) => (
                <tr key={`${m.source}-${m.destination}-${i}`}>
                  <td>{m.type || "—"}</td>
                  <td className="nx-mono truncate max-[560px]:overflow-visible max-[560px]:break-all max-[560px]:whitespace-normal" title={m.name}>
                    {m.name || "—"}
                  </td>
                  <td className="nx-mono truncate max-[560px]:overflow-visible max-[560px]:break-all max-[560px]:whitespace-normal" title={m.source}>
                    {m.source || "—"}
                  </td>
                  <td className="nx-mono truncate max-[560px]:overflow-visible max-[560px]:break-all max-[560px]:whitespace-normal" title={m.destination}>
                    {m.destination || "—"}
                  </td>
                  <td>{m.rw ? "RW" : "RO"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section title={t("完整配置（已脱敏）")}>
        <pre className="nx-pre m-3 whitespace-pre-wrap break-all">{vm.json}</pre>
      </Section>
    </div>
  );
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex min-w-0 items-baseline gap-2">
      <span className="shrink-0 text-[11px] text-neutral-500">{label}</span>
      <span className={`truncate max-[560px]:overflow-visible max-[560px]:break-all max-[560px]:whitespace-normal text-[12px] text-neutral-200 ${mono ? "nx-mono" : ""}`} title={value}>
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

export const CONTAINER_STATS_POLL_MS = 3000;

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
    refetchInterval: active ? CONTAINER_STATS_POLL_MS : false,
  });
  const ps = useQuery({
    queryKey: ["docker-ps", sessionId],
    queryFn: () => dockerApi.ps(sessionId),
  });

  if (stats.isPending) return <InsightLoading text={t("读取容器统计…")} />;
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
          {t("每 3 秒自动刷新 · 离开此页后停止 · 共")} {rows.length} {t("个运行中容器")}
        </span>
        {!containerRunning && <span className="nx-badge nx-badge-amber">{t("当前容器已停止")}</span>}
        <div className="nx-spacer" />
        {stats.isFetching && <span className="nx-hint">{t("正在刷新…")}</span>}
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void stats.refetch()}>
          <IconRefresh size={12} />
          {t("刷新")}
        </button>
      </div>

      {rows.length === 0 ? (
        <div className="nx-empty">
          {containerRunning ? t("暂无统计数据（每 3 秒刷新）") : t("当前容器已停止，只有运行中的容器才有统计")}
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto">
          <table className="nx-table nx-table-fixed min-w-[620px]">
            <thead>
              <tr>
                <th style={{ width: 160 }} className="left-0 z-[2] shadow-[inset_-1px_0_0_var(--nx-border)]">
                  {t("容器")}
                </th>
                <th>CPU %</th>
                <th>{t("内存用量")}</th>
                <th>{t("内存 %")}</th>
                <th>{t("网络 IO")}</th>
                <th>{t("块 IO")}</th>
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
                      {isCurrent && <span className="nx-badge nx-badge-green ml-1.5">{t("当前")}</span>}
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
          title={up ? t("上级：{value}", { value: redactContainerPath(up) }) : t("已经在根目录")}
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
                title={c.sensitive ? t("已脱敏") : redactContainerPath(c.path)}
              >
                {c.label}
              </button>
            </span>
          ))}
        </div>
        <button
          className="nx-tree-caret"
          title={t("刷新")}
          disabled={q.isFetching}
          onClick={() => void q.refetch()}
        >
          <IconRefresh size={12} />
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {q.isPending ? (
          <InsightLoading text={t("读取容器目录…")} />
        ) : q.isError ? (
          <InsightError
            error={redactPathInText(describeDockerError(q.error), path)}
            onRetry={() => void q.refetch()}
            extra={
              up ? (
                <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setPath(up)}>
                  {t("返回上级")}
                </button>
              ) : undefined
            }
          />
        ) : sorted.length === 0 ? (
          <div className="nx-empty">{t("该目录是空的")}</div>
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
                        ? t("已脱敏")
                        : isDir
                          ? redactContainerPath(joinContainerPath(path, name))
                          : t("{value}（仅列出目录内容，不读取文件）", { value: redactContainerPath(joinContainerPath(path, name)) })
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
                        <span className="nx-badge nx-badge-amber ml-1.5" title={t("疑似敏感文件：名称已脱敏，不提供内容读取")}>
                          {t("敏感")}
                        </span>
                      )}
                    </td>
                    <td style={{ width: 130 }} className="nx-hint nx-right">
                      {isDir ? t("目录") : t("文件")}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      <div className="shrink-0 border-t border-neutral-800/60 px-3 py-1.5">
        <span className="nx-hint">{t("仅列出容器目录内容，不读取文件")}</span>
      </div>
    </div>
  );
}
