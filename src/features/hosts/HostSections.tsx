// Sections of the host detail page. Each reads one existing API for one host and never connects on its own:
// listings that need a connection say so and wait for the user to open the workspace.

import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  connectAsset,
  DB_KIND_LABEL,
  dbKindOf,
  nextTabId,
  sessionStatusText,
  useSessionById,
  useUi,
  type Workspace,
} from "../../app/store";
import { dbApi, dockerApi, type Asset } from "../../ipc/commands";
import { createExecBatchId, execBatchApi, type ExecBatchHealthResult } from "../../ipc/execBatchApi";
import { describeError } from "../../ui/errorText";
import { IconActivity, IconBox, IconDatabase, IconPlay, IconRefresh, IconTerminal } from "../../ui/icons";
import { useBackgroundSessionsAccess } from "../terminal/backgroundSessionsAccess";
import { formatUptime } from "../fleet/format";
import { databaseWorkspaceOf, hostAddress } from "./hostDetail";

const MAX_CONTAINERS = 12;
const MAX_SCHEMAS = 24;
const HEALTH_TIMEOUT_MS = 30_000;

export function HostSection({
  title,
  icon,
  actions,
  children,
  testId,
}: {
  title: string;
  icon: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  testId?: string;
}) {
  return (
    <section className="nx-card flex min-w-0 flex-col gap-2 !p-3" aria-label={title} data-testid={testId}>
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span className="shrink-0 text-neutral-500">{icon}</span>
        <h3 className="nx-card-title m-0">{title}</h3>
        <span className="nx-spacer" />
        {actions}
      </div>
      {children}
    </section>
  );
}

export function SectionHint({ children }: { children: ReactNode }) {
  return <p className="nx-hint m-0 text-[12px]">{children}</p>;
}

function isConnected(status: string | undefined): boolean {
  return status === "connected";
}

const TAB_KIND_LABEL: Record<string, string> = {
  terminal: "终端",
  files: "文件",
  log: "日志",
  mount: "挂载",
  forward: "端口转发",
  docker: "容器",
  background: "后台会话",
};

// SessionsSection lists the host workspace's tabs; selecting one switches to it.
export function SessionsSection({ workspace }: { workspace: Workspace | undefined }) {
  const session = useSessionById(workspace?.sessionId);
  const backgroundAvailable = useBackgroundSessionsAccess(workspace);
  const setActiveTab = useUi((state) => state.setActiveTab);
  const tabs = workspace?.panes.flatMap((pane) => pane.tabs) ?? [];
  const openBackground = () => {
    if (!workspace?.sessionId) return;
    useUi.getState().addTab({
      id: `background-${workspace.id}`,
      kind: "background",
      title: "后台会话",
      sessionId: workspace.sessionId,
      closable: true,
    });
  };
  const status = workspace?.restorePending ? "未连接" : workspace?.sessionId ? sessionStatusText(session?.status) : "未连接";
  return (
    <HostSection
      title="会话"
      icon={<IconTerminal size={14} />}
      testId="host-sessions"
      actions={
        backgroundAvailable && workspace?.sessionId ? (
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={openBackground}>
            后台会话
          </button>
        ) : null
      }
    >
      {!workspace ? (
        <SectionHint>尚未打开该主机的工作区。</SectionHint>
      ) : (
        <>
          <div className="flex min-w-0 items-baseline gap-1 text-[12px] text-neutral-400">
            <span className="min-w-0 truncate" title={workspace.title}>
              工作区「{workspace.title}」
            </span>
            <span className="shrink-0">
              · <span data-testid="host-session-status">{status}</span>
            </span>
          </div>
          {tabs.length === 0 ? (
            <SectionHint>工作区中暂无标签页。</SectionHint>
          ) : (
            <ul className="m-0 flex list-none flex-col gap-0.5 p-0">
              {tabs.map((tab) => (
                <li key={tab.id}>
                  <button
                    type="button"
                    className="nx-row nx-row-oneline w-full text-left"
                    title={tab.title}
                    onClick={() => setActiveTab(tab.id)}
                  >
                    <span className="nx-row-name min-w-0 flex-auto truncate">{tab.title}</span>
                    <span className="nx-hint shrink-0 text-[11px]">{TAB_KIND_LABEL[tab.kind] ?? tab.kind}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </HostSection>
  );
}

// ContainersSection reads the host's containers through the workspace session; it needs a connected workspace.
export function ContainersSection({ workspace, visible }: { workspace: Workspace | undefined; visible: boolean }) {
  const session = useSessionById(workspace?.sessionId);
  const sessionId = workspace?.sessionId;
  const connected = Boolean(sessionId) && !workspace?.restorePending && isConnected(session?.status);
  const query = useQuery({
    queryKey: ["host-detail", "containers", sessionId],
    queryFn: () => dockerApi.ps(sessionId ?? ""),
    enabled: visible && connected,
    retry: false,
    staleTime: 15_000,
  });
  const openDocker = () => {
    if (!sessionId) return;
    useUi.getState().addTab({ id: nextTabId(`docker-${sessionId}`), kind: "docker", title: "容器", sessionId, closable: true });
  };
  const containers = query.data ?? [];
  return (
    <HostSection
      title="容器"
      icon={<IconBox size={14} />}
      testId="host-containers"
      actions={
        connected ? (
          <>
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-xs"
              aria-label="刷新容器"
              disabled={query.isFetching}
              onClick={() => void query.refetch()}
            >
              <IconRefresh size={11} className={query.isFetching ? "animate-spin" : ""} />
            </button>
            <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={openDocker}>
              容器管理
            </button>
          </>
        ) : null
      }
    >
      {!connected ? (
        <SectionHint>打开并连接工作区后可查看容器。</SectionHint>
      ) : query.isError ? (
        <p className="m-0 text-[12px] text-red-300">未能读取容器：{describeError(query.error)}</p>
      ) : query.isPending ? (
        <SectionHint>正在读取容器…</SectionHint>
      ) : containers.length === 0 ? (
        <SectionHint>暂无容器。</SectionHint>
      ) : (
        <>
          <ul className="m-0 flex list-none flex-col gap-1 p-0 text-[12px]">
            {containers.slice(0, MAX_CONTAINERS).map((container) => (
              <li key={container.id} className="flex min-w-0 items-center gap-2">
                <span
                  className={`nx-dot shrink-0 ${container.state === "running" ? "text-green-400" : "text-neutral-500"}`}
                  aria-hidden="true"
                />
                <span className="min-w-0 flex-auto truncate text-neutral-200" title={container.name}>
                  {container.name}
                </span>
                <span className="nx-hint min-w-0 max-w-[45%] shrink truncate text-[11px]" title={container.image}>
                  {container.image}
                </span>
                <span className="nx-hint shrink-0 text-[11px]">{container.state}</span>
              </li>
            ))}
          </ul>
          {containers.length > MAX_CONTAINERS && (
            <SectionHint>另有 {containers.length - MAX_CONTAINERS} 个容器，在容器管理中查看。</SectionHint>
          )}
        </>
      )}
    </HostSection>
  );
}

// SchemaList reads the schemas of an open, connected database workspace.
function SchemaList({ connId, visible }: { connId: string; visible: boolean }) {
  const query = useQuery({
    queryKey: ["host-detail", "schemas", connId],
    queryFn: () => dbApi.schemas(connId),
    enabled: visible,
    retry: false,
    staleTime: 30_000,
  });
  if (query.isError) return <div className="text-[11.5px] text-red-300">未能读取数据库列表：{describeError(query.error)}</div>;
  if (query.isPending) return <div className="nx-hint text-[11.5px]">正在读取数据库列表…</div>;
  const schemas = query.data ?? [];
  if (schemas.length === 0) return <div className="nx-hint text-[11.5px]">暂无可见的数据库。</div>;
  return (
    <div className="flex flex-wrap gap-1" aria-label="数据库列表">
      {schemas.slice(0, MAX_SCHEMAS).map((schema) => (
        <span key={schema} className="nx-badge max-w-full truncate" title={schema}>
          {schema}
        </span>
      ))}
      {schemas.length > MAX_SCHEMAS && <span className="nx-hint text-[11px]">另有 {schemas.length - MAX_SCHEMAS} 个</span>}
    </div>
  );
}

function DatabaseRow({ database, relation, visible }: { database: Asset; relation?: string; visible: boolean }) {
  const workspace = useUi((state) => databaseWorkspaceOf(state.workspaces, database.id));
  const dbKind = dbKindOf(database.kind);
  const open = Boolean(workspace?.connId) && !workspace?.restorePending;
  const address = hostAddress({ host: database.host, port: database.port, username: null });
  return (
    <li className="flex min-w-0 flex-col gap-1 border-t border-neutral-800/60 pt-1.5 first:border-t-0 first:pt-0">
      <div className="flex min-w-0 flex-wrap items-center gap-2 text-[12px]">
        <span className="min-w-0 max-w-full truncate font-medium text-neutral-100" title={database.name}>{database.name}</span>
        {dbKind && <span className="nx-badge shrink-0">{DB_KIND_LABEL[dbKind]}</span>}
        {address && <span className="nx-hint min-w-0 max-w-full truncate text-[11px]" title={address}>{address}</span>}
        {relation && <span className="nx-badge shrink-0">{relation}</span>}
        <span className="nx-spacer" />
        <span className={`shrink-0 text-[11px] ${open ? "text-green-300" : "text-neutral-500"}`}>{open ? "已连接" : "未连接"}</span>
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
          aria-label={`${open ? "切换到" : "打开"} ${database.name}`}
          onClick={() => void connectAsset(database)}
        >
          <IconPlay size={11} />
          {open ? "切换" : "打开"}
        </button>
      </div>
      {open && workspace?.connId && dbKind !== "redis" && <SchemaList connId={workspace.connId} visible={visible} />}
    </li>
  );
}

// DatabasesSection lists database assets on this host; an open connection also lists its schemas.
export function DatabasesSection({
  databases,
  relations,
  visible,
  emptyHint,
}: {
  databases: readonly Asset[];
  // relations maps a database id to why it is listed (同地址 or 经由此主机).
  relations?: Readonly<Record<string, string>>;
  visible: boolean;
  emptyHint: string;
}) {
  return (
    <HostSection title="数据库" icon={<IconDatabase size={14} />} testId="host-databases">
      {databases.length === 0 ? (
        <SectionHint>{emptyHint}</SectionHint>
      ) : (
        <ul className="m-0 flex list-none flex-col gap-1.5 p-0">
          {databases.map((database) => (
            <DatabaseRow key={database.id} database={database} relation={relations?.[database.id]} visible={visible} />
          ))}
        </ul>
      )}
    </HostSection>
  );
}

function percent(value: number | null | undefined): string {
  return value === null || value === undefined ? "—" : `${value.toFixed(1)}%`;
}

// SshHealthSection probes an SSH host on demand with the fleet health check; it reuses the open workspace session.
export function SshHealthSection({
  assetId,
  workspace,
  viaJump = false,
}: {
  assetId: string;
  workspace: Workspace | undefined;
  viaJump?: boolean;
}) {
  const session = useSessionById(workspace?.sessionId);
  const sessionId = isConnected(session?.status) ? session?.id : undefined;
  const [result, setResult] = useState<ExecBatchHealthResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const pending = useRef<{ batchId: string; assetId: string } | null>(null);

  // A different host or an unmounted page drops the pending probe and its result.
  useEffect(() => {
    setResult(null);
    setError(null);
    setRunning(false);
    return () => {
      const current = pending.current;
      pending.current = null;
      if (current) void execBatchApi.healthCancel(current.batchId).catch(() => undefined);
    };
  }, [assetId]);

  const probe = useCallback(async () => {
    if (pending.current) return;
    const request = { batchId: createExecBatchId(), assetId };
    pending.current = request;
    setRunning(true);
    setError(null);
    try {
      const batch = await execBatchApi.healthProbe({
        batchId: request.batchId,
        targets: [{ assetId, ...(sessionId ? { sessionId } : {}) }],
        timeoutMs: HEALTH_TIMEOUT_MS,
        maxConcurrent: 1,
      });
      if (pending.current !== request) return;
      setResult(batch.results.find((item) => item.assetId === assetId) ?? null);
    } catch (cause) {
      if (pending.current === request) setError(describeError(cause));
    } finally {
      if (pending.current === request) {
        pending.current = null;
        setRunning(false);
      }
    }
  }, [assetId, sessionId]);

  const rows: { label: string; value: string }[] = result
    ? [
        { label: "负载", value: result.load1 == null ? "—" : `${result.load1.toFixed(2)} / ${result.load5?.toFixed(2) ?? "—"} / ${result.load15?.toFixed(2) ?? "—"}` },
        { label: "内存", value: percent(result.memoryUsedPercent) },
        { label: "磁盘", value: percent(result.diskUsedPercent) },
        { label: "运行时长", value: result.uptimeSeconds == null ? "—" : formatUptime(result.uptimeSeconds) },
      ]
    : [];
  return (
    <HostSection
      title="指标"
      icon={<IconActivity size={14} />}
      testId="host-metrics"
      actions={
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={running} onClick={() => void probe()}>
          <IconRefresh size={11} className={running ? "animate-spin" : ""} />
          {result ? "重新检查" : "检查"}
        </button>
      }
    >
      {error ? (
        <p className="m-0 text-[12px] text-red-300">检查失败：{error}</p>
      ) : running && !result ? (
        <SectionHint>正在检查…</SectionHint>
      ) : !result ? (
        <>
          <SectionHint>
            {sessionId
              ? "通过当前会话读取负载、内存、磁盘和运行时长。"
              : "未连接时只检查可达性；打开工作区后可读取负载、内存、磁盘和运行时长。"}
          </SectionHint>
          {viaJump && !sessionId && <SectionHint>该主机经跳板机连接，检查时将登录跳板机。</SectionHint>}
        </>
      ) : !result.reachable ? (
        <p className="m-0 text-[12px] text-red-300">主机不可达{result.error ? `：${result.error}` : ""}</p>
      ) : result.error ? (
        <div className="flex flex-col gap-1 text-[12px]">
          <span className="text-green-300">可达</span>
          <span className="nx-hint">{result.error}</span>
        </div>
      ) : (
        <>
          <dl className="m-0 grid grid-cols-2 gap-x-4 gap-y-1.5 text-[12px]">
            {rows.map((row) => (
              <div key={row.label} className="min-w-0">
                <dt className="nx-hint text-[11px]">{row.label}</dt>
                <dd className="m-0 break-words font-mono text-neutral-100">{row.value}</dd>
              </div>
            ))}
          </dl>
          {result.metricErrors?.map((item) => (
            <div key={item.metric} className="text-[11px] text-red-300">
              {item.metric}：{item.error}
            </div>
          ))}
        </>
      )}
    </HostSection>
  );
}
