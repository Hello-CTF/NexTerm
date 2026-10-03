// `docker_stats` 输出的容错解析。
//
// 这个 RPC 的返回是「按行拼接的字符串」，而线上有三种合法形态：
//  - Go SDK 路径（internal/docker/format.go）：每行一个 JSON 对象，字段是
//    docker `{{json .}}` 的列名（ID/Name/CPUPerc/MemUsage/…）；
//  - CLI 回退路径（`docker stats --no-stream --format '{{json .}}'`）：同样是 JSON 行；
//  - 演示模式（src/demo/mock.ts）：`id12|name|cpu|mem` 管道分隔。
// 逐行先试 JSON、再退回管道切分，两种都能渲染；都认不出的行丢弃而不是炸掉整表。

export interface DockerStatsRow {
  /** 12 位短 id（与 docker_ps 的 ContainerSummary.id 同形，用于归属比对与高亮）。 */
  id: string;
  name: string;
  cpuPerc: string;
  memUsage: string;
  memPerc: string;
  netIO: string;
  blockIO: string;
  pids: string;
}

const EMPTY_ROW: Omit<DockerStatsRow, "id" | "name"> = {
  cpuPerc: "",
  memUsage: "",
  memPerc: "",
  netIO: "",
  blockIO: "",
  pids: "",
};

export function parseStatsOutput(raw: string): DockerStatsRow[] {
  const rows: DockerStatsRow[] = [];
  for (const line of raw.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const row = parseStatsLine(trimmed);
    if (row) rows.push(row);
  }
  return rows;
}

function parseStatsLine(line: string): DockerStatsRow | null {
  if (line.startsWith("{")) {
    try {
      const o = JSON.parse(line) as Record<string, unknown>;
      const id = str(o.ID) || str(o.Container);
      if (!id) return null;
      return {
        id,
        name: str(o.Name).replace(/^\//, ""),
        cpuPerc: str(o.CPUPerc),
        memUsage: str(o.MemUsage),
        memPerc: str(o.MemPerc),
        netIO: str(o.NetIO),
        blockIO: str(o.BlockIO),
        pids: str(o.PIDs),
      };
    } catch {
      return null;
    }
  }
  const parts = line.split("|");
  if (parts.length >= 4) {
    return {
      ...EMPTY_ROW,
      id: parts[0].trim(),
      name: parts[1].trim().replace(/^\//, ""),
      cpuPerc: parts[2].trim(),
      memUsage: parts[3].trim(),
    };
  }
  return null;
}

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}
