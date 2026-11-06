import { Fragment, useEffect, useMemo, useState } from "react";
import type { ComposeStack, ContainerSummary } from "../../../src/ipc/commands";
import { groupContainersByCompose } from "../../../src/features/docker/composeGrouping";
import { containerStateLabel } from "../../../src/features/docker/containerState";
import { createMergedLogBuffer, MERGED_LOG_LINE_LIMIT, MERGED_LOG_MAX_CONTAINERS } from "../../../src/features/docker/mergedLogs";
import {
  IconArrowLeft,
  IconBox,
  IconClose,
  IconDownload,
  IconEdit,
  IconList,
  IconMonitor,
  IconPlay,
  IconRefresh,
  IconRestart,
  IconStop,
  IconTerminal,
} from "../../../src/ui/icons";
import { ConfirmDialog, pick, usePrefersReducedMotion, useToasts, type DemoProps } from "./common";

const copy = {
  zh: {
    containers: "容器", images: "镜像", summary: (running: number, total: number) => `${running} 运行中 / 共 ${total}`,
    picked: (n: number) => `已选 ${n}`, clearPick: "取消选择", merged: "合并日志", mergedTitle: `按时间合并查看所选容器的日志（至少 2 个）`,
    refresh: "刷新", columns: ["名称", "状态", "镜像", "端口"], imageColumns: ["仓库 / 标签", "大小", "创建时间"],
    selectAll: "全选容器", select: (name: string) => `选择 ${name}`, ungrouped: "未分组容器",
    insight: "详情 / 统计 / 文件", logs: "查看日志", exec: "进入容器终端", execStopped: "容器已停止，启动后可进入终端", restart: "重启", stop: "停止", start: "启动",
    notInDemo: "示例中未包含该视图",
    stack: (n: number) => `Compose stack · ${n} services`,
    back: "返回", follow: "日志跟随", following: "跟随中 · 关闭此标签页或返回即停止",
    mergedHeader: (n: number) => `合并日志 · ${n} 个容器 · 仅保留最近 ${MERGED_LOG_LINE_LIMIT} 行`, closeMerged: "关闭合并日志",
    confirm: "确认", cancel: "取消", ok: "确定",
    stopConfirm: (name: string) => `停止容器 ${name}？\n\n容器内正在运行的进程会退出，它对外提供的服务会中断；容器本身和它的数据保留，之后可以重新启动。`,
    restartConfirm: (name: string) => `重启容器 ${name}？\n\n容器会先停止再启动，期间它对外提供的服务会中断；正在运行的进程会退出，容器数据保留。`,
    downConfirm: (project: string) => `停止并删除 Compose 栈 ${project} 的容器和默认网络？\n\n命名卷不会被删除；生产环境还需要逐目标输入实际环境名。`,
    done: (action: string, name: string) => `已${({ start: "启动", stop: "停止", restart: "重启" } as Record<string, string>)[action]}容器 ${name}`,
    composeDone: (project: string, action: string) => `${project} · ${action} 已完成`,
    reset: "重置演示", state: containerStateLabel,
  },
  en: {
    containers: "Containers", images: "Images", summary: (running: number, total: number) => `${running} running / ${total} total`,
    picked: (n: number) => `${n} selected`, clearPick: "Clear selection", merged: "Merged logs", mergedTitle: "View the selected containers' logs merged by time (at least 2)",
    refresh: "Refresh", columns: ["Name", "State", "Image", "Ports"], imageColumns: ["Repository / tag", "Size", "Created"],
    selectAll: "Select all containers", select: (name: string) => `Select ${name}`, ungrouped: "Ungrouped containers",
    insight: "Details / stats / files", logs: "View logs", exec: "Open a container terminal", execStopped: "The container is stopped; start it to open a terminal", restart: "Restart", stop: "Stop", start: "Start",
    notInDemo: "This view is not part of the demo",
    stack: (n: number) => `Compose stack · ${n} services`,
    back: "Back", follow: "Following logs", following: "Following · close this tab or go back to stop",
    mergedHeader: (n: number) => `Merged logs · ${n} containers · last ${MERGED_LOG_LINE_LIMIT} lines kept`, closeMerged: "Close merged logs",
    confirm: "Confirm", cancel: "Cancel", ok: "OK",
    stopConfirm: (name: string) => `Stop container ${name}?\n\nProcesses in the container exit and the services it provides stop; the container and its data are kept and it can be started again.`,
    restartConfirm: (name: string) => `Restart container ${name}?\n\nThe container stops and starts again, interrupting its services; running processes exit and the container's data is kept.`,
    downConfirm: (project: string) => `Stop and remove the containers and default network of Compose stack ${project}?\n\nNamed volumes are kept; production environments also require typing each environment name.`,
    done: (action: string, name: string) => `${action === "start" ? "Started" : action === "stop" ? "Stopped" : "Restarted"} container ${name}`,
    composeDone: (project: string, action: string) => `${project} · ${action} finished`,
    reset: "Reset demo", state: (state: string) => (state === "running" ? "Running" : state === "exited" ? "Stopped" : state),
  },
};

const initialContainers: ContainerSummary[] = [
  { id: "c1", name: "api-server", image: "nexterm/api:2.4", state: "running", status: "Up 6 hours (healthy)", ports: "127.0.0.1:8080->8080/tcp", composeProject: "api", composeService: "api" },
  { id: "c2", name: "api-postgres-1", image: "postgres:16", state: "running", status: "Up 6 hours", ports: "127.0.0.1:5432->5432/tcp", composeProject: "api", composeService: "postgres" },
  { id: "c3", name: "api-redis-1", image: "redis:7", state: "running", status: "Up 6 hours", ports: "6379/tcp", composeProject: "api", composeService: "redis" },
  { id: "c4", name: "backup-cron", image: "alpine:3.20", state: "exited", status: "Exited (0) 2 hours ago", ports: "", composeProject: null },
];

const stacks: ComposeStack[] = [{
  project: "api", workingDir: "/home/deploy/apps/api-server", configFiles: ["/home/deploy/apps/api-server/docker-compose.yml"],
  envPath: "/home/deploy/apps/api-server/.env", envExists: true, services: ["api", "postgres", "redis"], readOnly: false,
}];

const images = [
  ["nexterm/api:2.4", "182 MB", "3 days ago"],
  ["postgres:16", "432 MB", "2 weeks ago"],
  ["redis:7", "117 MB", "3 weeks ago"],
  ["alpine:3.20", "7.8 MB", "1 month ago"],
];

const logLines: Record<string, string[]> = {
  "api-server": [
    "2026-11-06T07:12:40.120Z INFO  GET /healthz 200 7ms",
    "2026-11-06T07:12:42.480Z INFO  GET /api/projects 200 39ms",
    "2026-11-06T07:12:44.031Z WARN  GET /api/reports 200 584ms",
    "2026-11-06T07:12:45.902Z INFO  worker queue=3 active=2",
  ],
  "api-postgres-1": [
    "2026-11-06T07:12:41.210Z LOG:  checkpoint starting: time",
    "2026-11-06T07:12:44.002Z LOG:  duration: 512.4 ms  statement: SELECT * FROM report_rows WHERE ...",
    "2026-11-06T07:12:46.870Z LOG:  checkpoint complete: wrote 42 buffers",
  ],
  "api-redis-1": [
    "2026-11-06T07:12:43.300Z 1:M * 100 changes in 300 seconds. Saving...",
    "2026-11-06T07:12:43.410Z 1:M * Background saving terminated with success",
  ],
  "backup-cron": ["2026-11-06T05:00:00.000Z backup: snapshot data-20261106-0500 created"],
};
const liveLines = ["INFO  GET /healthz 200 6ms", "INFO  GET /api/projects 200 41ms", "WARN  GET /api/reports 200 597ms", "INFO  worker queue=2 active=2"];
const PALETTE = ["is-sky", "is-green", "is-amber", "is-fuchsia"];

function stripTimestamp(line: string): string {
  return line.replace(/^\S+Z /, "");
}

type Copy = (typeof copy)["zh"] | (typeof copy)["en"];
type Pending = { kind: "stop" | "restart"; container: ContainerSummary } | { kind: "down"; project: string };

function FollowView({ container, t, onBack }: { container: ContainerSummary; t: Copy; onBack: () => void }) {
  const reduced = usePrefersReducedMotion();
  const [lines, setLines] = useState(() => (logLines[container.name] ?? []).map(stripTimestamp));
  useEffect(() => {
    if (reduced || container.state !== "running") return;
    let index = 0;
    const timer = window.setInterval(() => {
      setLines((current) => [...current.slice(-40), liveLines[index++ % liveLines.length]]);
    }, 1600);
    return () => window.clearInterval(timer);
  }, [reduced, container.state]);
  return <div className="nx-pane demo-follow">
    <div className="nx-toolbar">
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={onBack}><IconArrowLeft size={13} />{t.back}</button>
      <span className="nx-toolbar-title demo-truncate">{container.name}</span>
      <span className="nx-badge demo-hide-narrow">{t.follow}</span>
      <span className="nx-dot demo-dot-green" role="img" aria-label={t.following} />
      <span className="nx-spacer" />
      <span className="nx-hint demo-hide-narrow">{t.following}</span>
    </div>
    <pre className="demo-log" role="log" aria-label={t.follow}>{lines.join("\n")}</pre>
  </div>;
}

function MergedLogs({ containers, t, onClose }: { containers: ContainerSummary[]; t: Copy; onClose: () => void }) {
  const lines = useMemo(() => {
    const buffer = createMergedLogBuffer();
    containers.forEach((container, source) => buffer.push(source, logLines[container.name] ?? []));
    return buffer.snapshot();
  }, [containers]);
  return <div className="demo-overlay" onClick={onClose}>
    <div className="nx-modal demo-merged" role="dialog" aria-modal="true" aria-label={t.merged} onClick={(event) => event.stopPropagation()} onKeyDown={(event) => { if (event.key === "Escape") onClose(); }}>
      <div className="nx-modal-header">
        <span className="demo-truncate">{t.mergedHeader(containers.length)}</span>
        <span className="nx-spacer" />
        <button type="button" className="nx-icon-btn nx-icon-btn-sm" aria-label={t.closeMerged} autoFocus onClick={onClose}><IconClose size={12} /></button>
      </div>
      <div className="demo-merged-names">{containers.map((c, i) => <span key={c.id} className={PALETTE[i % PALETTE.length]}>{c.name}</span>)}</div>
      <div className="demo-log" role="log" aria-label={t.merged}>
        {lines.map((line) => <div key={line.seq}><span className={PALETTE[line.source % PALETTE.length]}>[{containers[line.source]?.name}]</span> {line.text}</div>)}
      </div>
    </div>
  </div>;
}

export default function DockerDemo({ language }: DemoProps) {
  const t = pick(language, copy);
  const { push: pushToast, view: toastView } = useToasts(language);
  const [tab, setTab] = useState<"containers" | "images">("containers");
  const [rows, setRows] = useState(initialContainers);
  const [pending, setPending] = useState<ReadonlySet<string>>(new Set());
  const [composeBusy, setComposeBusy] = useState(false);
  const [picked, setPicked] = useState<ReadonlySet<string>>(new Set());
  const [following, setFollowing] = useState<ContainerSummary | null>(null);
  const [merged, setMerged] = useState<ContainerSummary[] | null>(null);
  const [confirming, setConfirming] = useState<Pending | null>(null);

  const running = rows.filter((c) => c.state === "running").length;
  const pickedRows = rows.filter((c) => picked.has(c.id));
  const allPicked = rows.length > 0 && picked.size === rows.length;

  const markPending = (id: string, on: boolean) => setPending((current) => {
    const next = new Set(current);
    if (on) next.add(id);
    else next.delete(id);
    return next;
  });
  const runAction = (c: ContainerSummary, action: "start" | "stop" | "restart") => {
    markPending(c.id, true);
    window.setTimeout(() => {
      setRows((current) => current.map((row) => row.id !== c.id ? row : {
        ...row,
        state: action === "stop" ? "exited" : "running",
        status: action === "stop" ? (language === "zh" ? "Exited (0) 刚刚" : "Exited (0) just now") : "Up 1 second",
      }));
      markPending(c.id, false);
      pushToast("success", t.done(action, c.name));
    }, 1100);
  };
  const composeAction = (project: string, action: "up" | "restart" | "pull" | "down") => {
    setComposeBusy(true);
    window.setTimeout(() => {
      setComposeBusy(false);
      if (action === "down") {
        setRows((current) => current.filter((row) => row.composeProject !== project));
        setPicked(new Set());
      }
      pushToast("success", t.composeDone(project, action));
    }, 1200);
  };
  const togglePick = (id: string, on: boolean) => setPicked((current) => {
    const next = new Set(current);
    if (on) next.add(id);
    else next.delete(id);
    return next;
  });

  if (following) return <div className="demo-root">
    <FollowView key={following.id} container={following} t={t} onBack={() => setFollowing(null)} />
  </div>;

  return <div className="nx-pane demo-docker">
    <div className="nx-toolbar demo-wrap">
      <div className="nx-segment">
        <button type="button" className={`nx-segment-item${tab === "containers" ? " is-active" : ""}`} aria-pressed={tab === "containers"} onClick={() => setTab("containers")}><IconBox size={12} />{t.containers}<span className="nx-count">{rows.length}</span></button>
        <button type="button" className={`nx-segment-item${tab === "images" ? " is-active" : ""}`} aria-pressed={tab === "images"} onClick={() => setTab("images")}><IconList size={12} />{t.images}<span className="nx-count">{images.length}</span></button>
      </div>
      <span className="nx-hint demo-hide-narrow">{t.summary(running, rows.length)}</span>
      <span className="nx-spacer" />
      {tab === "containers" && picked.size > 0 && <>
        <span className="nx-hint">{t.picked(picked.size)}</span>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setPicked(new Set())}>{t.clearPick}</button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" title={t.mergedTitle} disabled={pickedRows.length < 2 || pickedRows.length > MERGED_LOG_MAX_CONTAINERS} onClick={() => setMerged(pickedRows)}><IconList size={13} />{t.merged}</button>
      </>}
      {rows.length < initialContainers.length && <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm demo-control" onClick={() => { setRows(initialContainers); setPicked(new Set()); }}>{t.reset}</button>}
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm"><IconRefresh size={13} />{t.refresh}</button>
    </div>
    <div className="demo-scroll">
      {tab === "images" ? <table className="nx-table">
        <thead><tr>{t.imageColumns.map((c) => <th key={c}>{c}</th>)}</tr></thead>
        <tbody>{images.map(([ref, size, created]) => <tr key={ref}><td className="nx-mono">{ref}</td><td className="nx-mono">{size}</td><td>{created}</td></tr>)}</tbody>
      </table> : <table className="nx-table nx-table-fixed demo-docker-table">
        <thead><tr>
          <th className="demo-col-check"><input className="nx-check" type="checkbox" aria-label={t.selectAll} checked={allPicked} ref={(el) => { if (el) el.indeterminate = picked.size > 0 && !allPicked; }} onChange={(e) => setPicked(e.target.checked ? new Set(rows.map((c) => c.id)) : new Set())} /></th>
          <th className="demo-col-name">{t.columns[0]}</th>
          <th className="demo-col-state">{t.columns[1]}</th>
          <th className="demo-col-image">{t.columns[2]}</th>
          <th className="demo-col-ports">{t.columns[3]}</th>
          <th className="demo-col-actions" />
        </tr></thead>
        <tbody>
          {groupContainersByCompose(rows, stacks).map((group) => <Fragment key={group.project ?? "ungrouped"}>
            <tr><td colSpan={6} className="demo-group-cell">
              {group.project ? <div className="demo-stack">
                <span className="nx-badge nx-badge-purple">{group.project}</span>
                <span className="nx-hint">{t.stack(group.stack?.services.length ?? group.containers.length)}</span>
                <span className="nx-spacer" />
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={composeBusy} onClick={() => composeAction(group.project as string, "up")}><IconPlay size={11} />up -d</button>
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={composeBusy} onClick={() => setConfirming({ kind: "down", project: group.project as string })}><IconStop size={11} />down</button>
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={composeBusy} onClick={() => composeAction(group.project as string, "restart")}><IconRestart size={11} />restart</button>
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={composeBusy} onClick={() => composeAction(group.project as string, "pull")}>{composeBusy ? <IconRefresh size={11} className="demo-spin" /> : <IconDownload size={11} />}pull</button>
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={composeBusy} onClick={() => setFollowing(group.containers[0])}><IconList size={11} />logs</button>
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={composeBusy} onClick={() => pushToast("info", t.notInDemo)}><IconEdit size={11} />Edit and redeploy</button>
              </div> : <span className="demo-ungrouped">{t.ungrouped}</span>}
            </td></tr>
            {group.containers.map((c) => {
              const busy = pending.has(c.id);
              const isRunning = c.state === "running";
              return <tr key={c.id} className={busy ? "is-busy" : undefined}>
                <td><input className="nx-check" type="checkbox" aria-label={t.select(c.name)} checked={picked.has(c.id)} onChange={(e) => togglePick(c.id, e.target.checked)} /></td>
                <td className="demo-cell-name" title={c.name}><IconBox size={13} />{c.name}{c.composeProject && <span className="nx-badge nx-badge-purple">{c.composeProject}</span>}</td>
                <td><span className="demo-state">
                  <span className={`nx-badge ${isRunning ? "nx-badge-green" : "nx-badge-red"}`}><span className="nx-dot" />{t.state(c.state)}</span>
                  <span className={`demo-status-text demo-hide-narrow${isRunning ? "" : " is-stopped"}`} title={c.status}>{c.status}</span>
                </span></td>
                <td className="nx-mono demo-truncate demo-col-image" title={c.image}>{c.image}</td>
                <td className="nx-mono demo-truncate demo-col-ports" title={c.ports || "—"}>{c.ports || "—"}</td>
                <td className="demo-row-actions">
                  <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t.insight} aria-label={`${t.insight} ${c.name}`} onClick={() => pushToast("info", t.notInDemo)}><IconMonitor size={13} /></button>
                  <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t.logs} aria-label={`${t.logs} ${c.name}`} onClick={() => setFollowing(c)}><IconList size={13} /></button>
                  <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={isRunning ? t.exec : t.execStopped} aria-label={`${t.exec} ${c.name}`} disabled={!isRunning} onClick={() => pushToast("info", t.notInDemo)}><IconTerminal size={13} /></button>
                  <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t.restart} aria-label={`${t.restart} ${c.name}`} disabled={busy} onClick={() => setConfirming({ kind: "restart", container: c })}>{busy ? <IconRefresh size={13} className="demo-spin" /> : <IconRestart size={13} />}</button>
                  {isRunning
                    ? <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t.stop} aria-label={`${t.stop} ${c.name}`} disabled={busy} onClick={() => setConfirming({ kind: "stop", container: c })}><IconStop size={12} /></button>
                    : <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t.start} aria-label={`${t.start} ${c.name}`} disabled={busy} onClick={() => runAction(c, "start")}><IconPlay size={12} /></button>}
                </td>
              </tr>;
            })}
          </Fragment>)}
        </tbody>
      </table>}
    </div>
    {merged && <MergedLogs containers={merged} t={t} onClose={() => setMerged(null)} />}
    {confirming && <ConfirmDialog
      title={t.confirm}
      message={confirming.kind === "down" ? t.downConfirm(confirming.project) : confirming.kind === "stop" ? t.stopConfirm(confirming.container.name) : t.restartConfirm(confirming.container.name)}
      cancel={t.cancel}
      ok={t.ok}
      danger
      onClose={(ok) => {
        const current = confirming;
        setConfirming(null);
        if (!ok) return;
        if (current.kind === "down") composeAction(current.project, "down");
        else runAction(current.container, current.kind);
      }}
    />}
    {toastView}
  </div>;
}
