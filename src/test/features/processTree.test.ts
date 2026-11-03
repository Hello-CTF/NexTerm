import { describe, expect, it } from "vitest";
import type { HostProcess } from "../../ipc/hostProcessesApi";
import { buildRows } from "../../features/hosts/processTree";

const p = (pid: number, ppid: number, extra: Partial<HostProcess> = {}): HostProcess => ({
  pid,
  ppid,
  cpu: 0,
  mem: 0,
  rssKiB: 0,
  elapsedSec: -1,
  name: `proc${pid}`,
  command: `/bin/proc${pid}`,
  ...extra,
});

const pids = (rows: { process: HostProcess }[]) => rows.map((r) => r.process.pid);
const base = { query: "", sort: "cpu" as const, tree: false, collapsed: new Set<number>() };

describe("buildRows flat", () => {
  it("sorts by cpu and by rss descending", () => {
    const list = [p(1, 0, { cpu: 1, rssKiB: 300 }), p(2, 1, { cpu: 9, rssKiB: 100 }), p(3, 1, { cpu: 5, rssKiB: 200 })];
    expect(pids(buildRows(list, base))).toEqual([2, 3, 1]);
    expect(pids(buildRows(list, { ...base, sort: "mem" }))).toEqual([1, 3, 2]);
  });

  it("filters by name, command, user and pid case-insensitively", () => {
    const list = [p(10, 1, { name: "Nginx", user: "www" }), p(20, 1, { command: "/usr/bin/REDIS" }), p(30, 1, { user: "Root" })];
    expect(pids(buildRows(list, { ...base, query: "nginx" }))).toEqual([10]);
    expect(pids(buildRows(list, { ...base, query: "redis" }))).toEqual([20]);
    expect(pids(buildRows(list, { ...base, query: "root" }))).toEqual([30]);
    expect(pids(buildRows(list, { ...base, query: "20" }))).toEqual([20]);
  });
});

describe("buildRows tree", () => {
  const tree = { ...base, tree: true };

  it("nests children under parents sorted by the current order, orphans become roots", () => {
    const list = [p(1, 0, { cpu: 1 }), p(2, 1, { cpu: 2 }), p(3, 1, { cpu: 8 }), p(4, 3), p(9, 777, { cpu: 0.5 })];
    const rows = buildRows(list, tree);
    expect(pids(rows)).toEqual([1, 3, 4, 2, 9]);
    expect(rows.map((r) => r.depth)).toEqual([0, 1, 2, 1, 0]);
    expect(rows[0].hasChildren).toBe(true);
    expect(rows[2].hasChildren).toBe(false);
  });

  it("hides descendants of collapsed nodes", () => {
    const list = [p(1, 0), p(2, 1), p(3, 2)];
    const rows = buildRows(list, { ...tree, collapsed: new Set([1]) });
    expect(pids(rows)).toEqual([1]);
    expect(rows[0].expanded).toBe(false);
  });

  it("terminates on self parents and cycles and still lists every process once", () => {
    const list = [p(5, 5), p(6, 7), p(7, 6), p(8, 7)];
    const rows = buildRows(list, tree);
    expect([...pids(rows)].sort((a, b) => a - b)).toEqual([5, 6, 7, 8]);
  });

  it("keeps matches with their ancestors when searching, even under collapsed nodes", () => {
    const list = [p(1, 0), p(2, 1), p(3, 2, { name: "target" }), p(4, 1, { name: "other" })];
    const rows = buildRows(list, { ...tree, query: "target", collapsed: new Set([1]) });
    expect(pids(rows)).toEqual([1, 2, 3]);
  });
});
