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

function isIPv4(value: string): boolean {
  const parts = value.split(".");
  return parts.length === 4 && parts.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255);
}

function isIPv6(value: string): boolean {
  if (!/^[0-9a-fA-F:.]+$/.test(value) || !value.includes(":")) return false;
  const halves = value.split("::");
  if (halves.length > 2) return false;
  const groups = (half: string) => (half === "" ? [] : half.split(":"));
  const all = halves.flatMap(groups);
  let units = 0;
  for (let i = 0; i < all.length; i++) {
    const group = all[i];
    if (group.includes(".")) {
      // An embedded IPv4 address is only valid as the final group and counts as two.
      if (i !== all.length - 1 || !isIPv4(group)) return false;
      units += 2;
    } else if (/^[0-9a-fA-F]{1,4}$/.test(group)) {
      units += 1;
    } else {
      return false;
    }
  }
  return halves.length === 2 ? units < 8 : units === 8;
}

function isIPLiteral(value: string): boolean {
  return value === "" || isIPv4(value) || isIPv6(value);
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
    if (!isIPLiteral(hostIp)) continue;
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
