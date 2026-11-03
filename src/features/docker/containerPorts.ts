export interface PublishedPort {
  hostIp: string;
  hostPort: number;
  containerPort: number;
  protocol: string;
}

const MAX_RANGE_EXPANSION = 16;

function parseRange(value: string): [number, number] | null {
  const match = /^(\d+)(?:-(\d+))?$/.exec(value);
  if (!match) return null;
  const start = Number(match[1]);
  const end = match[2] === undefined ? start : Number(match[2]);
  if (!Number.isInteger(start) || !Number.isInteger(end) || start < 1 || end < start || end > 65535) return null;
  return [start, end];
}

function stripBrackets(address: string): string {
  return address.startsWith("[") && address.endsWith("]") ? address.slice(1, -1) : address;
}

// Parses ContainerSummary.ports (moby and docker CLI formats). Only published tcp ports are returned, one entry per
// host port with IPv4 bindings preferred; ranges expand to at most 16 ports.
export function parsePublishedPorts(ports: string): PublishedPort[] {
  const byHostPort = new Map<number, PublishedPort>();
  for (const raw of ports.split(",")) {
    const entry = raw.trim();
    const arrow = entry.indexOf("->");
    if (arrow < 0) continue;
    const hostSide = entry.slice(0, arrow);
    const [containerSide, protocol = "tcp"] = entry.slice(arrow + 2).split("/");
    if (protocol !== "tcp") continue;
    const colon = hostSide.lastIndexOf(":");
    const hostIp = colon < 0 ? "" : stripBrackets(hostSide.slice(0, colon));
    const hostRange = parseRange(colon < 0 ? hostSide : hostSide.slice(colon + 1));
    const containerRange = parseRange(containerSide);
    if (!hostRange || !containerRange) continue;
    const count = Math.min(hostRange[1] - hostRange[0] + 1, MAX_RANGE_EXPANSION);
    for (let offset = 0; offset < count; offset++) {
      const hostPort = hostRange[0] + offset;
      const containerPort = Math.min(containerRange[0] + offset, containerRange[1]);
      const existing = byHostPort.get(hostPort);
      if (existing && (!existing.hostIp.includes(":") || hostIp.includes(":"))) continue;
      byHostPort.set(hostPort, { hostIp, hostPort, containerPort, protocol });
    }
  }
  return [...byHostPort.values()];
}

// The address to dial on the container host for a published binding.
export function dialHostFor(hostIp: string): string {
  const address = stripBrackets(hostIp.trim());
  return address === "" || address === "0.0.0.0" || address === "::" ? "127.0.0.1" : address;
}
