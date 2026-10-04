
export interface DockerStatsRow {
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
