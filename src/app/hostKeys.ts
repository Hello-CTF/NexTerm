import { assetApi, sessionApi } from "../ipc/commands";
import type { SshHostKeyProbeDto } from "../ipc/types";
import { ask } from "../ui/dialogs";

export type HostKeyDetail = {
  host?: string;
  port?: number;
  keyType?: string;
  fingerprint?: string;
  key?: string;
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

function ensureCurrent(isCurrent?: () => boolean): void {
  if (isCurrent && !isCurrent()) {
    throw { code: "account_changed", message: "账号已切换，旧连接已取消" };
  }
}

export async function trustPresentedHostKey(detail: HostKeyDetail | undefined, isCurrent?: () => boolean): Promise<boolean> {
  if (!detail?.host || !detail?.port || !detail?.keyType || !detail?.fingerprint || !detail.key) return false;
  ensureCurrent(isCurrent);
  await assetApi.knownHostAccept(detail.host, detail.port, detail.keyType, detail.fingerprint, detail.key);
  return true;
}

export async function connectWithHostKeyConfirm<T>(connect: () => Promise<T>, isCurrent?: () => boolean): Promise<T | null> {
  try {
    ensureCurrent(isCurrent);
    return await connect();
  } catch (e) {
    ensureCurrent(isCurrent);
    const pending = hostKeyPendingError(e);
    if (!pending) throw e;
    const confirmed = await confirmHostKey(pending.detail);
    ensureCurrent(isCurrent);
    if (!confirmed) return null;
    if (!(await trustPresentedHostKey(pending.detail, isCurrent))) throw e;
    try {
      ensureCurrent(isCurrent);
      return await connect();
    } catch (retryError) {
      const retryPending = hostKeyPendingError(retryError);
      if (retryPending) {
        const detail = retryPending.detail;
        if (!detail?.host || !detail.port || !detail.fingerprint) throw retryPending;
        const previous = (detail?.known ?? []).map((key) => key.fingerprint).filter(Boolean).join("、");
        throw {
          ...retryPending,
          message: detail?.changed
            ? `主机密钥确认后仍发生变更：${detail.host}:${detail.port}，原指纹 ${previous}，当前指纹 ${detail.fingerprint}。请核对主机后重试。`
            : `主机密钥确认后仍待验证：${detail?.host}:${detail?.port}，当前指纹 ${detail?.fingerprint}。请核对主机后重试。`,
        };
      }
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
    key: probe.key,
    changed: probe.state === "changed",
    known: probe.known,
  };
}

export async function confirmHostKeyIfNeeded(
  assetId: string,
  kind: string | undefined,
  isCurrent?: () => boolean,
): Promise<boolean> {
  if (kind !== "ssh" && kind !== "docker") return true;
  ensureCurrent(isCurrent);
  let probe: SshHostKeyProbeDto;
  try {
    probe = await sessionApi.probeHostKey(assetId);
  } catch (e) {
    ensureCurrent(isCurrent);
    const pending = hostKeyPendingError(e);
    if (!pending) return true;
    const confirmed = await confirmHostKey(pending.detail);
    ensureCurrent(isCurrent);
    if (!confirmed) return false;
    return trustPresentedHostKey(pending.detail, isCurrent);
  }
  ensureCurrent(isCurrent);
  if (probe.state === "known") return true;
  const detail = hostKeyDetailFromProbe(probe);
  const confirmed = await confirmHostKey(detail);
  ensureCurrent(isCurrent);
  if (!confirmed) return false;
  return trustPresentedHostKey(detail, isCurrent);
}
