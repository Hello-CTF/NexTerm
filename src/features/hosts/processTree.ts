import type { HostProcess } from "../../ipc/hostProcessesApi";

export type ProcessSort = "cpu" | "mem";

export interface ProcessRow {
  process: HostProcess;
  depth: number;
  hasChildren: boolean;
  expanded: boolean;
}

export interface BuildOptions {
  query: string;
  sort: ProcessSort;
  tree: boolean;
  collapsed: ReadonlySet<number>;
}

export function matchesQuery(p: HostProcess, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return (
    p.name.toLowerCase().includes(q) ||
    (p.argName ?? "").toLowerCase().includes(q) ||
    p.command.toLowerCase().includes(q) ||
    (p.user ?? "").toLowerCase().includes(q) ||
    String(p.pid).includes(q)
  );
}

function comparator(sort: ProcessSort) {
  return (a: HostProcess, b: HostProcess) => {
    const d = sort === "mem" ? b.rssKiB - a.rssKiB : b.cpu - a.cpu;
    return d !== 0 ? d : a.pid - b.pid;
  };
}

export function buildRows(processes: readonly HostProcess[], opts: BuildOptions): ProcessRow[] {
  const cmp = comparator(opts.sort);
  const searching = opts.query.trim() !== "";
  if (!opts.tree) {
    return processes
      .filter((p) => matchesQuery(p, opts.query))
      .sort(cmp)
      .map((process) => ({ process, depth: 0, hasChildren: false, expanded: false }));
  }

  const byPid = new Map<number, HostProcess>();
  for (const p of processes) if (!byPid.has(p.pid)) byPid.set(p.pid, p);
  const parentOf = (p: HostProcess) => (p.ppid !== p.pid ? byPid.get(p.ppid) : undefined);

  // Matches plus their ancestors; the visited set stops the walk on a cycle.
  let included: Set<number> | null = null;
  if (searching) {
    included = new Set();
    for (const p of byPid.values()) {
      if (!matchesQuery(p, opts.query)) continue;
      const seen = new Set<number>();
      for (let cur: HostProcess | undefined = p; cur && !seen.has(cur.pid); cur = parentOf(cur)) {
        seen.add(cur.pid);
        included.add(cur.pid);
      }
    }
  }

  const nodes = [...byPid.values()].filter((p) => !included || included.has(p.pid)).sort(cmp);
  const children = new Map<number, HostProcess[]>();
  const roots: HostProcess[] = [];
  for (const p of nodes) {
    const parent = parentOf(p);
    if (parent && (!included || included.has(parent.pid))) {
      const list = children.get(parent.pid);
      if (list) list.push(p);
      else children.set(parent.pid, [p]);
    } else roots.push(p);
  }

  const rows: ProcessRow[] = [];
  const visited = new Set<number>();
  const walk = (start: HostProcess) => {
    const stack: { p: HostProcess; depth: number }[] = [{ p: start, depth: 0 }];
    while (stack.length > 0) {
      const { p, depth } = stack.pop()!;
      if (visited.has(p.pid)) continue;
      visited.add(p.pid);
      const kids = (children.get(p.pid) ?? []).filter((k) => !visited.has(k.pid));
      const expanded = searching || !opts.collapsed.has(p.pid);
      rows.push({ process: p, depth, hasChildren: kids.length > 0, expanded });
      if (expanded) for (let i = kids.length - 1; i >= 0; i--) stack.push({ p: kids[i], depth: depth + 1 });
    }
  };
  for (const r of roots) walk(r);
  // Members of a cycle have no root above them: surface each as a root so nothing disappears.
  for (const p of nodes) if (!visited.has(p.pid) && !isHiddenByCollapse(p)) walk(p);
  return rows;

  function isHiddenByCollapse(p: HostProcess): boolean {
    // A node under a collapsed ancestor is intentionally absent; a cycle member is not.
    const seen = new Set<number>([p.pid]);
    for (let cur = parentOf(p); cur; cur = parentOf(cur)) {
      if (seen.has(cur.pid)) return false;
      seen.add(cur.pid);
      if (visited.has(cur.pid)) return true;
    }
    return false;
  }
}
