import { IconCode, IconKey, IconLock, IconShieldCheck } from "../../ui/icons";

type Icon = typeof IconKey;

export interface KindMeta {
  label: string;
  Icon: Icon;
  tone: string;
  bg: string;
}

export const KIND_META: Record<string, KindMeta> = {
  password: { label: "密码", Icon: IconLock, tone: "text-blue-400", bg: "bg-blue-500/12" },
  private_key: { label: "私钥", Icon: IconKey, tone: "text-purple-400", bg: "bg-purple-500/12" },
  api_key: { label: "API Key", Icon: IconCode, tone: "text-neutral-400", bg: "bg-white/[.06]" },
  passphrase: {
    label: "私钥口令（旧）",
    Icon: IconShieldCheck,
    tone: "text-purple-300",
    bg: "bg-purple-500/10",
  },
};

export const NEW_KIND_ORDER = ["password", "private_key", "api_key"];

export function kindMeta(kind: string): KindMeta {
  return (
    KIND_META[kind] ?? {
      label: kind,
      Icon: IconKey,
      tone: "text-neutral-400",
      bg: "bg-white/[.06]",
    }
  );
}

export const KIND_ORDER = Object.keys(KIND_META);

const pad = (n: number) => String(n).padStart(2, "0");

export function formatTime(ms: number): string {
  const d = new Date(ms);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(
    d.getMinutes(),
  )}`;
}
