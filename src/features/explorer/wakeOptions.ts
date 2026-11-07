import { t } from "../../i18n";
// Wake-on-LAN options stored on a network asset. The backend validates the same rules when the asset is saved and
// again when a packet is sent; this module only gives the form early feedback.

export interface WakeOptions {
  mac: string;
  broadcast: string;
  // Kept as text so a half-typed value does not flicker; empty means the default (9).
  port: string;
}

export const DEFAULT_WAKE_BROADCAST = "255.255.255.255";
export const DEFAULT_WAKE_PORT = 9;

const MAC_PATTERN = /^[0-9A-Fa-f]{2}([:-])[0-9A-Fa-f]{2}(\1[0-9A-Fa-f]{2}){4}$/;

export function validateWakeMac(value: string): string | null {
  const text = value.trim();
  if (!text) return null;
  if (!MAC_PATTERN.test(text)) return t("MAC 地址格式无效，请使用 AA:BB:CC:DD:EE:FF 或 AA-BB-CC-DD-EE-FF");
  const digits = text.replace(/[:-]/g, "").toLowerCase();
  if (/^0+$/.test(digits) || /^f+$/.test(digits)) return t("MAC 地址不能是全 0 或全 F");
  return null;
}

export function validateWakeBroadcast(value: string): string | null {
  const text = value.trim();
  if (!text) return null;
  const parts = text.split(".");
  // No leading zeros: "010" is ambiguous (octal in some tools) and the backend refuses it.
  const octets = parts.map((part) => (/^(0|[1-9]\d{0,2})$/.test(part) ? Number(part) : NaN));
  if (parts.length !== 4 || octets.some((n) => !(n >= 0 && n <= 255))) return t("广播地址必须是 IPv4 地址（例如 192.168.1.255）");
  const [a, b, c, d] = octets;
  if (a === 255 && b === 255 && c === 255 && d === 255) return null;
  if (
    a === 0 ||
    a === 127 ||
    (a === 169 && b === 254) ||
    (a === 100 && b >= 64 && b <= 127) ||
    a >= 224
  ) {
    return t("广播地址不能是 0.0.0.0/8、回环、链路本地、组播、100.64.0.0/10 或 240.0.0.0/4 地址");
  }
  return null;
}

export function validateWakePort(value: string): string | null {
  const text = value.trim();
  if (!text) return null;
  const port = Number(text);
  if (!/^\d+$/.test(text) || port < 1 || port > 65535) return t("唤醒端口必须在 1 到 65535 之间");
  return null;
}

export interface WakeErrors {
  mac: string | null;
  broadcast: string | null;
  port: string | null;
}

export function validateWakeOptions(values: WakeOptions): WakeErrors {
  const mac = validateWakeMac(values.mac);
  // Broadcast and port mean nothing without a MAC, so they are not validated (and not saved) then.
  if (!values.mac.trim()) return { mac: null, broadcast: null, port: null };
  return { mac, broadcast: validateWakeBroadcast(values.broadcast), port: validateWakePort(values.port) };
}

export function hasWakeErrors(errors: WakeErrors): boolean {
  return Boolean(errors.mac || errors.broadcast || errors.port);
}

export function readWakeOptions(options: Record<string, unknown> | undefined): WakeOptions {
  const raw = options ?? {};
  return {
    mac: typeof raw.wakeMac === "string" ? raw.wakeMac : "",
    broadcast: typeof raw.wakeBroadcast === "string" ? raw.wakeBroadcast : "",
    port: typeof raw.wakePort === "number" && Number.isInteger(raw.wakePort) ? String(raw.wakePort) : "",
  };
}

// writeWakeOptions applies the form values on top of the existing options; with no MAC all three keys are dropped.
export function writeWakeOptions(base: Record<string, unknown>, values: WakeOptions): Record<string, unknown> {
  const options = { ...base };
  delete options.wakeMac;
  delete options.wakeBroadcast;
  delete options.wakePort;
  const mac = values.mac.trim();
  if (!mac) return options;
  options.wakeMac = mac;
  if (values.broadcast.trim()) options.wakeBroadcast = values.broadcast.trim();
  if (values.port.trim()) options.wakePort = Number(values.port.trim());
  return options;
}

export function canWake(options: Record<string, unknown> | undefined): boolean {
  return readWakeOptions(options).mac.trim() !== "";
}
