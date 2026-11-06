import { useCallback, useMemo, useState } from "react";
import type { HostProcess } from "../../../src/ipc/hostProcessesApi";
import type { HostListener } from "../../../src/ipc/hostPortsApi";
import { buildRows, type ProcessSort } from "../../../src/features/hosts/processTree";
import { usePaging, useListFocus, type ListFocus } from "../../../src/features/hosts/listControls";
import { isLoopback, PORT_SCOPES, SCOPE_LABELS, SCOPE_TITLES, type PortScope } from "../../../src/features/hosts/portScope";
import { IconActivity, IconChevronDown, IconChevronRight, IconNetwork, IconRefresh } from "../../../src/ui/icons";
import { ConfirmDialog, pick, useToasts, type DemoProps, type Language } from "./common";

const HOST = "web-01";

const copy = {
  zh: {
    ports: "端口", processes: "进程", refresh: "刷新", portSearch: "按端口、地址、进程或 PID 搜索", processSearch: "按名称、命令、用户或 PID 搜索",
    total: (n: number) => `共 ${n} 个`, matched: (m: number, n: number) => `匹配 ${m} / ${n} 个`, scopeGroup: "端口范围",
    scopes: SCOPE_LABELS, scopeTitles: SCOPE_TITLES,
    portColumns: ["端口", "协议", "绑定地址", "进程"], loopback: "仅本机", showProcess: "在进程列表中查看", forward: "转发并打开", forwardToast: "演示不建立端口转发",
    processColumns: ["PID", "名称", "用户", "CPU%", "内存", "运行时长", "状态", "监听端口"], sort: "排序", tree: "树形", memory: "内存",
    showPort: "在端口列表中查看", end: "结束", forceEnd: "强制结束", collapse: "折叠", expand: "展开",
    prev: "上一页", next: "下一页", page: (p: number, n: number) => `第 ${p} / ${n} 页`, pager: "进程分页",
    confirm: "确认", cancel: "取消", ok: "确定",
    killConfirm: (p: HostProcess, force: boolean) => `将在 ${HOST} 上${force ? "强制结束" : "结束"}进程 ${p.name}（PID ${p.pid}），执行：\nkill -${force ? "KILL" : "TERM"} ${p.pid}${force ? "\n\n强制结束会直接终止进程，进程不会有机会清理。" : ""}`,
    killed: (p: HostProcess, signal: string) => `已向进程 ${p.name}（PID ${p.pid}）发送 ${signal}。`,
  },
  en: {
    ports: "Ports", processes: "Processes", refresh: "Refresh", portSearch: "Search by port, address, process or PID", processSearch: "Search by name, command, user or PID",
    total: (n: number) => `${n} total`, matched: (m: number, n: number) => `${m} of ${n} match`, scopeGroup: "Port scope",
    scopes: { all: "All", exposed: "Exposed", loopback: "Local only" } as Record<PortScope, string>,
    scopeTitles: { all: "All listening ports", exposed: "Bound to a wildcard or interface address; other machines may reach it", loopback: "Bound to 127.0.0.1 or ::1 only; only this host can reach it" } as Record<PortScope, string>,
    portColumns: ["Port", "Protocol", "Bound address", "Process"], loopback: "Local only", showProcess: "Show in the process list", forward: "Forward and open", forwardToast: "The demo does not open port forwards",
    processColumns: ["PID", "Name", "User", "CPU%", "Memory", "Uptime", "State", "Listening"], sort: "Sort", tree: "Tree", memory: "Memory",
    showPort: "Show in the port list", end: "End", forceEnd: "Force end", collapse: "Collapse", expand: "Expand",
    prev: "Previous", next: "Next", page: (p: number, n: number) => `Page ${p} of ${n}`, pager: "Process pages",
    confirm: "Confirm", cancel: "Cancel", ok: "OK",
    killConfirm: (p: HostProcess, force: boolean) => `${force ? "Force end" : "End"} process ${p.name} (PID ${p.pid}) on ${HOST} by running:\nkill -${force ? "KILL" : "TERM"} ${p.pid}${force ? "\n\nForce ending terminates the process without giving it a chance to clean up." : ""}`,
    killed: (p: HostProcess, signal: string) => `Sent ${signal} to process ${p.name} (PID ${p.pid}).`,
  },
};

function proc(pid: number, ppid: number, name: string, user: string, cpu: number, mem: number, rssKiB: number, elapsedSec: number, command: string, stat = "S"): HostProcess {
  return { pid, ppid, name, user, cpu, mem, rssKiB, elapsedSec, command, stat };
}

const DAY = 86400;
const baseProcesses: HostProcess[] = [
  proc(1, 0, "systemd", "root", 0.0, 0.2, 12840, 41 * DAY, "/sbin/init"),
  proc(2, 0, "kthreadd", "root", 0.0, 0.0, 0, 41 * DAY, "[kthreadd]"),
  proc(301, 1, "systemd-journal", "root", 0.1, 0.3, 24120, 41 * DAY, "/lib/systemd/systemd-journald"),
  proc(455, 1, "systemd-resolve", "systemd-resolve", 0.0, 0.2, 13220, 41 * DAY, "/lib/systemd/systemd-resolved"),
  proc(520, 1, "cron", "root", 0.0, 0.0, 2810, 41 * DAY, "/usr/sbin/cron -f"),
  proc(612, 1, "sshd", "root", 0.0, 0.1, 8020, 41 * DAY, "sshd: /usr/sbin/sshd -D"),
  proc(2201, 612, "sshd", "deploy", 0.0, 0.1, 7110, 3 * 3600, "sshd: deploy@pts/0"),
  proc(2210, 2201, "bash", "deploy", 0.0, 0.1, 5240, 3 * 3600, "-bash"),
  proc(701, 1, "dockerd", "root", 0.6, 1.1, 92800, 41 * DAY, "/usr/bin/dockerd -H fd://"),
  proc(1290, 1, "containerd-shim", "root", 0.1, 0.2, 13400, 6 * 3600, "/usr/bin/containerd-shim-runc-v2 -namespace moby -id 8f2c"),
  proc(842, 1290, "api-server", "app", 4.8, 10.4, 831488, 6 * 3600, "/app/api-server --config /etc/api/config.yaml", "Ssl"),
  proc(812, 1, "nginx", "root", 0.0, 0.1, 9800, 12 * DAY, "nginx: master process /usr/sbin/nginx"),
  proc(813, 812, "nginx", "www-data", 0.4, 0.2, 14200, 12 * DAY, "nginx: worker process"),
  proc(814, 812, "nginx", "www-data", 0.3, 0.2, 13960, 12 * DAY, "nginx: worker process"),
  proc(905, 1290, "postgres", "postgres", 0.7, 1.6, 129024, 6 * 3600, "postgres -c shared_buffers=256MB"),
  proc(911, 905, "postgres", "postgres", 0.2, 0.4, 32100, 6 * 3600, "postgres: checkpointer"),
  proc(912, 905, "postgres", "postgres", 0.1, 0.3, 21000, 6 * 3600, "postgres: background writer"),
  proc(913, 905, "postgres", "postgres", 1.9, 0.9, 74800, 2 * 60 + 14, "postgres: app app 172.18.0.4(51822) SELECT", "R"),
  proc(930, 1290, "redis-server", "redis", 0.2, 0.2, 18432, 6 * 3600, "redis-server *:6379", "Ssl"),
  proc(2240, 2210, "htop", "deploy", 0.8, 0.1, 4800, 25 * 60, "htop", "S+"),
];
// Kernel worker threads fill the list past one page, like a real host.
const kworkers: HostProcess[] = Array.from({ length: 40 }, (_, i) => proc(20 + i, 2, `kworker/${Math.floor(i / 4)}:${i % 4}`, "root", 0, 0, 0, 41 * DAY, `[kworker/${Math.floor(i / 4)}:${i % 4}]`, "I"));
const allProcesses = [...baseProcesses, ...kworkers];

const listeners: HostListener[] = [
  { proto: "tcp", address: "0.0.0.0", port: 22, pid: 612, process: "sshd" },
  { proto: "tcp", address: "127.0.0.53", port: 53, pid: 455, process: "systemd-resolve" },
  { proto: "tcp", address: "0.0.0.0", port: 80, pid: 812, process: "nginx" },
  { proto: "tcp", address: "0.0.0.0", port: 443, pid: 812, process: "nginx" },
  { proto: "tcp", address: "127.0.0.1", port: 5432, pid: 905, process: "postgres" },
  { proto: "tcp6", address: "[::]", port: 6379, pid: 930, process: "redis-server" },
  { proto: "tcp", address: "127.0.0.1", port: 8080, pid: 842, process: "api-server" },
];

function elapsedText(language: Language, s: number): string {
  const d = Math.floor(s / DAY);
  const h = Math.floor((s % DAY) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (language === "zh") return d > 0 ? `${d}天${h}小时` : h > 0 ? `${h}小时${m}分` : m > 0 ? `${m}分${s % 60}秒` : `${s}秒`;
  return d > 0 ? `${d}d ${h}h` : h > 0 ? `${h}h ${m}m` : m > 0 ? `${m}m ${s % 60}s` : `${s}s`;
}

function rssText(kib: number): string {
  if (kib >= 1048576) return `${(kib / 1048576).toFixed(1)} GiB`;
  if (kib >= 1024) return `${(kib / 1024).toFixed(1)} MiB`;
  return `${kib} KiB`;
}

type Copy = (typeof copy)["zh"] | (typeof copy)["en"];

function Pager({ t, page, pages, onPage }: { t: Copy; page: number; pages: number; onPage: (page: number) => void }) {
  if (pages <= 1) return null;
  return <div className="demo-pager" role="group" aria-label={t.pager}>
    <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={page === 0} onClick={() => onPage(page - 1)}>{t.prev}</button>
    <span className="nx-hint" aria-live="polite">{t.page(page + 1, pages)}</span>
    <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={page >= pages - 1} onClick={() => onPage(page + 1)}>{t.next}</button>
  </div>;
}

function PortsCard({ t, focus, onShowProcess, onForward }: { t: Copy; focus: ListFocus<number> | null; onShowProcess: (pid: number) => void; onForward: () => void }) {
  const [scope, setScope] = useState<PortScope>("all");
  const [query, setQuery] = useState("");
  const counts = useMemo(() => {
    const loopback = listeners.filter((l) => isLoopback(l.address)).length;
    return { all: listeners.length, exposed: listeners.length - loopback, loopback };
  }, []);
  const matched = useMemo(() => {
    const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
    return listeners
      .filter((l) => scope === "all" || (scope === "loopback") === isLoopback(l.address))
      .filter((l) => terms.every((term) => [String(l.port), l.proto, l.address, l.process ?? "", String(l.pid ?? "")].some((field) => field.toLowerCase().includes(term))));
  }, [query, scope]);
  const paging = usePaging(matched, `${query}|${scope}`);
  const clearFilters = useCallback(() => {
    setQuery("");
    setScope("all");
  }, []);
  const highlight = useListFocus({
    focus,
    indexOf: (port) => matched.findIndex((l) => l.port === port),
    filtered: query !== "" || scope !== "all",
    clearFilters,
    setPage: paging.setPage,
    pageOf: paging.pageOf,
    rowSelector: useCallback((port: number) => `[data-demo="ports"] tr[data-port="${port}"]`, []),
  });
  return <section className="demo-card" aria-label={t.ports} data-demo="ports">
    <div className="demo-card-head"><IconNetwork size={14} /><h3>{t.ports}</h3><span className="nx-spacer" /><button type="button" className="nx-btn nx-btn-ghost nx-btn-xs"><IconRefresh size={11} />{t.refresh}</button></div>
    <div className="demo-list-toolbar">
      <input className="nx-input" type="search" aria-label={t.ports} placeholder={t.portSearch} value={query} onChange={(e) => setQuery(e.target.value)} />
      <div className="nx-segment" role="group" aria-label={t.scopeGroup}>
        {PORT_SCOPES.map((key) => <button key={key} type="button" className={`nx-segment-item${scope === key ? " is-active" : ""}`} aria-pressed={scope === key} title={t.scopeTitles[key]} onClick={() => setScope(key)}>{t.scopes[key]} {counts[key]}</button>)}
      </div>
      <span className="nx-hint">{matched.length === listeners.length ? t.total(listeners.length) : t.matched(matched.length, listeners.length)}</span>
    </div>
    <div className="demo-hscroll">
      <table className="nx-table">
        <thead><tr>{t.portColumns.map((c) => <th key={c}>{c}</th>)}<th /></tr></thead>
        <tbody>{paging.shown.map((l) => <tr key={`${l.proto}|${l.address}|${l.port}`} data-port={l.port} className={highlight === l.port ? "is-focused" : undefined}>
          <td className="nx-mono">{l.port}</td>
          <td>{l.proto}</td>
          <td className="nx-mono">{l.address}{isLoopback(l.address) && <span className="nx-badge demo-gap">{t.loopback}</span>}</td>
          <td><span className="demo-inline">{l.process}{l.pid !== undefined && <button type="button" className="nx-link nx-mono" title={t.showProcess} aria-label={`${t.showProcess} ${l.pid}`} onClick={() => onShowProcess(l.pid as number)}>{l.pid}</button>}</span></td>
          <td className="demo-nowrap"><button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={onForward}>{t.forward}</button></td>
        </tr>)}</tbody>
      </table>
    </div>
  </section>;
}

function ProcessesCard({ t, language, focus, onShowPort }: { t: Copy; language: Language; focus: ListFocus<number> | null; onShowPort: (port: number) => void }) {
  const [processes, setProcesses] = useState(allProcesses);
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<ProcessSort>("cpu");
  const [tree, setTree] = useState(true);
  const [collapsed, setCollapsed] = useState<ReadonlySet<number>>(new Set());
  const [killing, setKilling] = useState<{ process: HostProcess; force: boolean } | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const rows = useMemo(() => buildRows(processes, { query, sort, tree, collapsed }), [processes, query, sort, tree, collapsed]);
  const paging = usePaging(rows, `${query}|${sort}|${tree}`);
  const clearFilters = useCallback(() => setQuery(""), []);
  const highlight = useListFocus({
    focus,
    indexOf: (pid) => rows.findIndex((row) => row.process.pid === pid),
    filtered: query !== "",
    clearFilters,
    setPage: paging.setPage,
    pageOf: paging.pageOf,
    rowSelector: useCallback((pid: number) => `[data-demo="processes"] tr[data-pid="${pid}"]`, []),
  });
  const portsByPid = useMemo(() => {
    const map = new Map<number, HostListener[]>();
    for (const l of listeners) if (l.pid !== undefined) map.set(l.pid, [...(map.get(l.pid) ?? []), l]);
    return map;
  }, []);
  const toggle = (pid: number) => setCollapsed((prev) => {
    const next = new Set(prev);
    if (!next.delete(pid)) next.add(pid);
    return next;
  });
  return <section className="demo-card" aria-label={t.processes} data-demo="processes">
    <div className="demo-card-head"><IconActivity size={14} /><h3>{t.processes}</h3><span className="nx-spacer" /><button type="button" className="nx-btn nx-btn-ghost nx-btn-xs"><IconRefresh size={11} />{t.refresh}</button></div>
    <div className="demo-list-toolbar">
      <input className="nx-input" type="search" aria-label={t.processes} placeholder={t.processSearch} value={query} onChange={(e) => setQuery(e.target.value)} />
      <div className="nx-segment" role="group" aria-label={t.sort}>
        {(["cpu", "mem"] as const).map((k) => <button key={k} type="button" className={`nx-segment-item${sort === k ? " is-active" : ""}`} aria-pressed={sort === k} onClick={() => setSort(k)}>{k === "cpu" ? "CPU" : t.memory}</button>)}
      </div>
      <label className="demo-check-label"><input type="checkbox" checked={tree} onChange={(e) => setTree(e.target.checked)} />{t.tree}</label>
      <span className="nx-hint">{query ? t.matched(rows.length, processes.length) : t.total(processes.length)}</span>
    </div>
    {message && <p className="demo-ok-message" role="status">{message}</p>}
    <div className="demo-hscroll">
      <table className="nx-table">
        <thead><tr>{t.processColumns.map((c) => <th key={c}>{c}</th>)}<th /></tr></thead>
        <tbody>{paging.shown.map(({ process: p, depth, hasChildren, expanded }) => <tr key={p.pid} data-pid={p.pid} className={highlight === p.pid ? "is-focused" : undefined}>
          <td className="nx-mono">{p.pid}</td>
          <td title={p.command}><span className="demo-inline" style={{ paddingLeft: depth * 14 }}>
            {tree && (hasChildren
              ? <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs demo-caret" aria-label={`${expanded ? t.collapse : t.expand} ${p.pid}`} aria-expanded={expanded} onClick={() => toggle(p.pid)}>{expanded ? <IconChevronDown size={11} /> : <IconChevronRight size={11} />}</button>
              : <span className="demo-caret-space" />)}
            {p.name}
          </span></td>
          <td>{p.user}</td>
          <td className="nx-mono">{p.cpu.toFixed(1)}</td>
          <td className="nx-mono">{`${p.mem.toFixed(1)}% · ${rssText(p.rssKiB)}`}</td>
          <td className="demo-nowrap">{elapsedText(language, p.elapsedSec)}</td>
          <td>{p.stat}</td>
          <td>{(portsByPid.get(p.pid) ?? []).length === 0 ? "-" : (portsByPid.get(p.pid) ?? []).map((l) => <button key={`${l.address}|${l.port}`} type="button" className="nx-link nx-mono demo-gap-right" title={t.showPort} aria-label={`${t.showPort} ${l.port}`} onClick={() => onShowPort(l.port)}>{l.port}</button>)}</td>
          <td className="demo-nowrap">{p.pid > 1 && <>
            <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" aria-label={`${t.end} ${p.pid}`} onClick={() => setKilling({ process: p, force: false })}>{t.end}</button>
            <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" aria-label={`${t.forceEnd} ${p.pid}`} onClick={() => setKilling({ process: p, force: true })}>{t.forceEnd}</button>
          </>}</td>
        </tr>)}</tbody>
      </table>
    </div>
    <Pager t={t} page={paging.page} pages={paging.pages} onPage={paging.setPage} />
    {killing && <ConfirmDialog title={t.confirm} message={t.killConfirm(killing.process, killing.force)} cancel={t.cancel} ok={t.ok} onClose={(ok) => {
      const current = killing;
      setKilling(null);
      if (!ok) return;
      setProcesses((list) => list.filter((p) => p.pid !== current.process.pid));
      setMessage(t.killed(current.process, current.force ? "KILL" : "TERM"));
    }} />}
  </section>;
}

export default function DiagnosticsDemo({ language }: DemoProps) {
  const t = pick(language, copy);
  const { push: pushToast, view: toastView } = useToasts(language);
  const [processFocus, setProcessFocus] = useState<ListFocus<number> | null>(null);
  const [portFocus, setPortFocus] = useState<ListFocus<number> | null>(null);
  const showProcess = (pid: number) => setProcessFocus((current) => ({ value: pid, nonce: (current?.nonce ?? 0) + 1 }));
  const showPort = (port: number) => setPortFocus((current) => ({ value: port, nonce: (current?.nonce ?? 0) + 1 }));
  return <div className="nx-pane demo-diagnostics">
    <div className="nx-toolbar">
      <IconActivity size={14} className="demo-muted" />
      <span className="nx-toolbar-title">{HOST}</span>
      <span className="nx-badge nx-badge-green"><span className="nx-dot" />{language === "zh" ? "已连接" : "Connected"}</span>
    </div>
    <div className="demo-scroll demo-cards">
      <PortsCard t={t} focus={portFocus} onShowProcess={showProcess} onForward={() => pushToast("info", t.forwardToast)} />
      <ProcessesCard t={t} language={language} focus={processFocus} onShowPort={showPort} />
    </div>
    {toastView}
  </div>;
}
