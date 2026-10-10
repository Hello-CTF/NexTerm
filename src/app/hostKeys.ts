import { assetApi, sessionApi } from "../ipc/commands";
import type { SshHostKeyProbeDto } from "../ipc/types";
import { ask } from "../ui/dialogs";

export type HostKeyDetail = {
  host?: string;
  port?: number;
  keyType?: string;
  fingerprint?: string;
  changed?: boolean;
  known?: { keyType?: string; fingerprint?: string }[];
};

type HostKeyPendingError = {
  code: string;
  message: string;
  detail?: HostKeyDetail;
};

export function hostKeyPendingError(e: unknown): HostKeyPendingError | null {
  const err = e as HostKeyPendingError | null;
  return err && err.code === "host_key_pending" ? err : null;
}

export function hostKeyQuestion(detail: HostKeyDetail | undefined): string {
  if (detail?.changed) {
    const previous = (detail.known ?? [])
      .map((known) => known.fingerprint)
      .filter((fingerprint): fingerprint is string => Boolean(fingerprint))
      .join("、");
    return `主机 ${detail.host}:${detail.port} 的密钥已变更\n原指纹（SHA256）：${previous}\n新指纹（SHA256）：${detail.fingerprint}\n主机密钥类型：${detail.keyType}\n信任新指纹并继续？新指纹将替换已记录的旧指纹。如果此次变更不符合预期，请取消并核实服务器，否则可能连接到冒充的主机。`;
  }
  return `首次连接 ${detail?.host}:${detail?.port}\n主机密钥类型：${detail?.keyType}\n指纹（SHA256）：${detail?.fingerprint}\n信任并继续？指纹将保存到已知主机列表，后续连接会自动核对；核对失败时将再次阻止连接。`;
}

const inflightConfirms = new Map<string, Promise<boolean>>();

export function confirmHostKey(detail: HostKeyDetail | undefined): Promise<boolean> {
  const key = `${detail?.host ?? ""}:${detail?.port ?? 0}:${detail?.fingerprint ?? ""}`;
  const existing = inflightConfirms.get(key);
  if (existing) return existing;
  const promise = ask(hostKeyQuestion(detail), {
    title: detail?.changed ? "主机密钥变更警告" : "确认主机指纹",
    kind: "warning",
  }).finally(() => {
    inflightConfirms.delete(key);
  });
  inflightConfirms.set(key, promise);
  return promise;
}

export async function trustPresentedHostKey(detail: HostKeyDetail | undefined): Promise<boolean> {
  if (!detail?.host || !detail?.port || !detail?.keyType || !detail?.fingerprint) return false;
  await assetApi.knownHostAccept(detail.host, detail.port, detail.keyType, detail.fingerprint);
  return true;
}

export async function connectWithHostKeyConfirm<T>(connect: () => Promise<T>): Promise<T | null> {
  try {
    return await connect();
  } catch (e) {
    const pending = hostKeyPendingError(e);
    if (!pending) throw e;
    if (!(await confirmHostKey(pending.detail))) return null;
    if (!(await trustPresentedHostKey(pending.detail))) throw e;
    try {
      return await connect();
    } catch (retryError) {
      if (hostKeyPendingError(retryError) !== null) return null;
      throw retryError;
    }
  }
}

export function hostKeyDetailFromProbe(probe: SshHostKeyProbeDto): HostKeyDetail {
  return {
    host: probe.host,
    port: probe.port,
    keyType: probe.keyType,
    fingerprint: probe.fingerprint,
    changed: probe.state === "changed",
    known: probe.known,
  };
}

export async function confirmHostKeyIfNeeded(
  assetId: string,
  kind: string | undefined,
): Promise<boolean> {
  if (kind !== "ssh" && kind !== "docker") return true;
  let probe: SshHostKeyProbeDto;
  try {
    probe = await sessionApi.probeHostKey(assetId);
  } catch (e) {
    const pending = hostKeyPendingError(e);
    if (!pending) return true;
    if (!(await confirmHostKey(pending.detail))) return false;
    return trustPresentedHostKey(pending.detail);
  }
  if (probe.state === "known") return true;
  const detail = hostKeyDetailFromProbe(probe);
  if (!(await confirmHostKey(detail))) return false;
  return trustPresentedHostKey(detail);
}
