export type QuickConnectProtocol = "rdp" | "vnc";

export interface QuickConnectTarget {
  host: string;
  port: number;
  username: string | null;
  // Absent for SSH, the default; rdp:// and vnc:// targets carry their protocol.
  protocol?: QuickConnectProtocol;
}

const DEFAULT_SSH_PORT = 22;
const DEFAULT_PORT: Record<QuickConnectProtocol, number> = { rdp: 3389, vnc: 5900 };

export function parseQuickConnectTarget(raw: string): QuickConnectTarget | null {
  let input = raw.trim();
  if (!input) return null;
  let explicitScheme = false;
  let protocol: QuickConnectProtocol | undefined;
  const scheme = /^([a-z][a-z0-9+.-]*):\/\//i.exec(input);
  if (scheme) {
    const name = scheme[1].toLowerCase();
    if (name === "rdp" || name === "vnc") protocol = name;
    else if (name !== "ssh") return null;
    explicitScheme = true;
    input = input.slice(scheme[0].length);
  }
  if (input.includes("/") || /\s/.test(input)) return null;

  let username: string | null = null;
  const at = input.lastIndexOf("@");
  if (at >= 0) {
    username = input.slice(0, at);
    input = input.slice(at + 1);
    if (!username) return null;
  }

  let host = input;
  let port = protocol ? DEFAULT_PORT[protocol] : DEFAULT_SSH_PORT;
  let explicitPort = false;
  if (input.startsWith("[")) {
    const close = input.indexOf("]");
    if (close < 0) return null;
    host = input.slice(1, close);
    const rest = input.slice(close + 1);
    if (rest) {
      if (!rest.startsWith(":")) return null;
      const parsed = parsePort(rest.slice(1));
      if (parsed === null) return null;
      port = parsed;
      explicitPort = true;
    }
  } else {
    const colon = input.lastIndexOf(":");
    if (colon >= 0) {
      if (input.indexOf(":") !== colon) return null;
      const parsed = parsePort(input.slice(colon + 1));
      if (parsed === null) return null;
      host = input.slice(0, colon);
      port = parsed;
      explicitPort = true;
    }
  }
  if (!host) return null;
  if (!explicitScheme && !username && !explicitPort) return null;
  // VNC has no user name in the URL form this parser accepts.
  if (protocol === "vnc" && username) return null;
  return protocol ? { host, port, username, protocol } : { host, port, username };
}

function parsePort(raw: string): number | null {
  if (!/^\d+$/.test(raw)) return null;
  const port = Number(raw);
  if (port < 1 || port > 65535) return null;
  return port;
}

export function formatQuickConnectTarget(target: QuickConnectTarget): string {
  const host = target.host.includes(":") ? `[${target.host}]` : target.host;
  const base = target.username ? `${target.username}@${host}` : host;
  const defaultPort = target.protocol ? DEFAULT_PORT[target.protocol] : DEFAULT_SSH_PORT;
  const address = target.port === defaultPort ? base : `${base}:${target.port}`;
  return target.protocol ? `${target.protocol}://${address}` : address;
}
