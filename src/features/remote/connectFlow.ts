import type {
  RemoteDesktopCertificateStatus,
  RemoteDesktopPrepareResult,
  RemoteDesktopSecretResult,
} from "../../ipc/generated/types";
import { describeRdpFailure, describeVncFailure, IRON_ACCESS_DENIED, IRON_LOGON_FAILURE, IRON_NEGOTIATION, IRON_PROXY_CONNECT, IRON_RDCLEANPATH, IRON_WRONG_PASSWORD, isCredsspText } from "./rdpFailure";
import { RemoteEngineError, type EngineFailure, type RemoteClient, type RemoteSecret } from "./remoteTypes";

// retryable: a network drop worth reconnecting automatically; auth, certificate and permission failures never are,
// because retrying cannot fix them and would only repeat a rejected login.
export type FailureKind = "retryable" | "auth" | "certificate" | "permission" | "ended" | "fatal";

export interface Failure {
  kind: FailureKind;
  // One actionable sentence; the pane shows it under the title.
  message: string;
  // Replaces the generic title of the kind when the failure is recognised.
  title?: string;
  // Raw engine text for the collapsed details.
  detail?: string;
  code?: string;
}

const PERMISSION_CODES = new Set(["vault_locked", "unauthorized", "forbidden", "permission_denied", "account_changed"]);
const AUTH_TEXT = /认证失败|密码|authentication|password|credential/i;
const CERT_TEXT = /证书|指纹|certificate|fingerprint/i;

// textOf is the user-facing wording of a failure: the title replaces the generic one for its kind, the advice is the
// message, and the raw engine text sits in the collapsed details.
function textOf(input: EngineFailure): Pick<Failure, "title" | "message" | "detail"> {
  const text = input.protocol === "vnc" ? describeVncFailure(input) : describeRdpFailure(input);
  return { title: text.title, message: text.advice, detail: text.detail || undefined };
}

export function classifyFailure(input: EngineFailure): Failure {
  const base = { message: input.message || "连接失败", code: input.code };
  if (input.code === "needs_confirm") return { ...base, kind: "certificate" };
  if (input.code && PERMISSION_CODES.has(input.code)) return { ...base, kind: "permission" };
  if (input.code && input.stage !== "closed" && input.protocol && input.rdpKind === undefined) {
    // An application error from prepare or the IPC layer: nothing a retry fixes.
    return { ...base, kind: "fatal" };
  }
  if (input.protocol === "rdp") {
    // A CredSSP failure is the server rejecting the login, whatever kind the engine gave it.
    if (isCredsspText(input.detail ?? input.message)) return { ...base, ...textOf(input), kind: "auth" };
    if (input.rdpKind !== undefined) {
      const text = textOf(input);
      switch (input.rdpKind) {
        case IRON_WRONG_PASSWORD:
        case IRON_LOGON_FAILURE:
          return { ...base, ...text, kind: "auth" };
        case IRON_ACCESS_DENIED:
          return { ...base, ...text, kind: "permission" };
        case IRON_RDCLEANPATH:
          if (input.httpStatus === 403) return { ...base, ...text, kind: "certificate" };
          if (input.httpStatus === 401) return { ...base, ...text, kind: "retryable" };
          if (input.tlsAlert !== undefined) return { ...base, ...text, kind: "fatal" };
          return { ...base, ...text, kind: input.wasConnected ? "retryable" : "fatal" };
        case IRON_PROXY_CONNECT:
          return { ...base, ...text, kind: input.wasConnected ? "retryable" : "fatal" };
        case IRON_NEGOTIATION:
          return { ...base, ...text, kind: "fatal" };
        default:
          return { ...base, ...text, kind: input.wasConnected ? "retryable" : "fatal" };
      }
    }
  }
  if (input.stage === "security") {
    const text = textOf(input);
    if (CERT_TEXT.test(input.message)) return { ...base, ...text, kind: "certificate" };
    if (AUTH_TEXT.test(input.message)) return { ...base, ...text, kind: "auth" };
    return { ...base, ...text, kind: "fatal" };
  }
  if (input.stage === "closed") {
    if (input.clean) return { ...base, kind: "ended", message: input.message || "远程桌面已断开" };
    return { ...base, ...textOf(input), kind: input.wasConnected ? "retryable" : "fatal" };
  }
  if (input.protocol === "rdp" || input.protocol === "vnc") return { ...base, ...textOf(input), kind: "fatal" };
  return { ...base, kind: "fatal" };
}

export const MAX_AUTO_RECONNECTS = 5;
export const RETRY_DELAYS_MS = [2000, 4000, 8000, 15000, 30000] as const;

// retryDelayMs is the wait before reconnect attempt number `attempt` (0-based), or null when attempts are used up.
export function retryDelayMs(attempt: number): number | null {
  return attempt >= 0 && attempt < MAX_AUTO_RECONNECTS ? RETRY_DELAYS_MS[attempt] : null;
}

// A password asked at connect time is not kept, so an automatic reconnect would have to ask again without the user
// being there. Those sessions wait for the user instead.
export function mayAutoReconnect(failure: Failure, ctx: { promptPassword: boolean; attempt: number }): boolean {
  return failure.kind === "retryable" && !ctx.promptPassword && retryDelayMs(ctx.attempt) !== null;
}

export interface FlowApi {
  prepare(assetId: string): Promise<RemoteDesktopPrepareResult>;
  certificate(assetId: string): Promise<RemoteDesktopCertificateStatus>;
  trust(assetId: string, fingerprint: string): Promise<unknown>;
  setPassword(ticket: string, password: string): Promise<unknown>;
  releaseSecret(ticket: string): Promise<RemoteDesktopSecretResult>;
}

export interface FlowUi {
  // Resolves to the typed password, or null when the user canceled.
  askPassword(prepared: RemoteDesktopPrepareResult): Promise<string | null>;
  confirmCertificate(status: RemoteDesktopCertificateStatus): Promise<boolean>;
}

export type OpenClient = (prepared: RemoteDesktopPrepareResult, secret: RemoteSecret | null) => Promise<RemoteClient>;

export type FlowResult =
  | { ok: true; client: RemoteClient; prepared: RemoteDesktopPrepareResult; prompted: boolean }
  | { ok: false; failure: Failure; canceled?: boolean; prompted: boolean };

const MAX_ROUNDS = 4;

function engineFailureOf(error: unknown, protocol: EngineFailure["protocol"]): EngineFailure {
  if (error instanceof RemoteEngineError) return error.failure;
  const record = (error ?? {}) as { code?: unknown; message?: unknown };
  return {
    protocol,
    stage: "connect",
    message: typeof record.message === "string" && record.message ? record.message : "连接失败",
    code: typeof record.code === "string" ? record.code : undefined,
  };
}

function shouldCheckCertificate(failure: Failure, raw: EngineFailure): boolean {
  return failure.kind === "certificate" || (raw.protocol === "vnc" && raw.stage === "security");
}

function isForbidden(error: unknown): boolean {
  return (error as { code?: unknown } | null)?.code === "forbidden";
}

// The relay answers a refused certificate and a bad or expired ticket with the same RDCleanPath 403.
function isRdcleanpath403(raw: EngineFailure): boolean {
  return raw.protocol === "rdp" && raw.rdpKind === 4 && raw.httpStatus === 403;
}

// runConnectFlow takes one asset to an open client. Every attempt asks for a fresh single-use ticket. A certificate
// the user has not trusted is shown after the failed attempt (the backend refuses the connection first), and on
// confirmation the whole flow runs again with a new ticket. A ticket lives 60 seconds and is made before the password
// prompt, so a rejected ticket is retried once with a new one; the typed password stays in memory for that retry only.
// The caller can cancel at any time: the flow checks after every await, so a closed tab never releases a secret or
// opens a transport after the user went away.
export async function runConnectFlow(
  deps: { api: FlowApi; ui: FlowUi; open: OpenClient; cancelled?: () => boolean },
  assetId: string,
  options: { forcePrompt?: boolean } = {},
): Promise<FlowResult> {
  let forcePrompt = options.forcePrompt === true;
  let prompted = false;
  let staleRetried = false;
  let kept: string | null = null;
  const cancelledResult = (): FlowResult => ({ ok: false, canceled: true, prompted, failure: { kind: "ended", message: "已取消" } });
  const isCancelled = () => deps.cancelled?.() === true;
  try {
    for (let round = 0; round < MAX_ROUNDS; round += 1) {
      if (isCancelled()) return cancelledResult();
      let prepared: RemoteDesktopPrepareResult;
      try {
        prepared = await deps.api.prepare(assetId);
      } catch (error) {
        if (isCancelled()) return cancelledResult();
        const raw = engineFailureOf(error, "rdp");
        return { ok: false, prompted, failure: classifyFailure({ ...raw, rdpKind: undefined }) };
      }
      if (isCancelled()) return cancelledResult();
      const protocol = prepared.protocol === "vnc" ? "vnc" : "rdp";
      const needPrompt = prepared.promptPassword || forcePrompt || kept !== null || (protocol === "rdp" && !prepared.hasPassword && !prepared.noPassword);
      let typed: string | null = null;
      let secret: RemoteSecret | null = null;
      try {
        if (needPrompt) {
          if (kept !== null) {
            typed = kept;
          } else {
            typed = await deps.ui.askPassword(prepared);
            prompted = true;
            if (isCancelled()) return cancelledResult();
          }
          if (typed === null) return cancelledResult();
          kept = typed;
          if (protocol === "vnc") {
            await deps.api.setPassword(prepared.ticket, typed);
            if (isCancelled()) return cancelledResult();
          }
        }
        if (protocol === "rdp") {
          if (needPrompt) {
            secret = { username: prepared.username, domain: prepared.domain, password: typed ?? "" };
          } else {
            const released = await deps.api.releaseSecret(prepared.ticket);
            if (isCancelled()) return cancelledResult();
            secret = { username: released.username, domain: released.domain, password: released.password };
          }
        }
        const client = await deps.open(prepared, secret);
        if (isCancelled()) {
          try {
            client.disconnect();
          } catch {
            // Already closing.
          }
          return cancelledResult();
        }
        return { ok: true, client, prepared, prompted };
      } catch (error) {
        if (isCancelled()) return cancelledResult();
        // The ticket expired between prepare and the call that used it: one retry with a new ticket.
        if (isForbidden(error) && !staleRetried) {
          staleRetried = true;
          continue;
        }
        const raw = engineFailureOf(error, protocol);
        let failure = classifyFailure(raw);
        if (shouldCheckCertificate(failure, raw)) {
          let status: RemoteDesktopCertificateStatus | null = null;
          try {
            status = await deps.api.certificate(assetId);
          } catch {
            status = null;
          }
          if (isCancelled()) return cancelledResult();
          if (status && (status.status === "new" || status.status === "changed")) {
            if (!(await deps.ui.confirmCertificate(status))) {
              return {
                ok: false,
                canceled: true,
                prompted,
                failure: { kind: "certificate", message: "未信任服务器证书，连接已取消" },
              };
            }
            if (isCancelled()) return cancelledResult();
            try {
              await deps.api.trust(assetId, status.fingerprint);
            } catch (trustError) {
              return { ok: false, prompted, failure: classifyFailure(engineFailureOf(trustError, protocol)) };
            }
            continue;
          }
          // No certificate to confirm, so the 403 was the ticket: it expired while the user was typing.
          if (isRdcleanpath403(raw)) {
            if (!staleRetried) {
              staleRetried = true;
              continue;
            }
            failure = { kind: "fatal", message: "连接被中继拒绝（票据已失效），请重新连接", code: raw.code };
          }
        }
        // A VNC server that wants a password the asset does not hold: ask once instead of failing.
        if (failure.kind === "auth" && protocol === "vnc" && !prepared.hasPassword && !prepared.noPassword && !needPrompt) {
          forcePrompt = true;
          continue;
        }
        if (isForbidden(error)) failure = { kind: "permission", message: failure.message, code: "forbidden" };
        return { ok: false, prompted, failure };
      } finally {
        typed = null;
        secret = null;
      }
    }
    return { ok: false, prompted, failure: { kind: "fatal", message: "服务器证书确认后仍无法连接" } };
  } finally {
    kept = null;
  }
}

export function describeCertificate(status: RemoteDesktopCertificateStatus): { title: string; body: string } {
  const lines = [`主机：${status.host}:${status.port}`];
  if (status.subject) lines.push(`证书主体：${status.subject}`);
  if (status.notAfterMs) lines.push(`有效期至：${new Date(status.notAfterMs).toLocaleString()}`);
  if (status.status === "changed") {
    lines.push(`原指纹（SHA-256）：${status.previousFingerprint ?? "未知"}`);
    lines.push(`新指纹（SHA-256）：${status.fingerprint}`);
    return {
      title: "服务器证书已变更",
      body: `${lines.join("\n")}\n\n警告：该服务器的证书与此前信任的不同。如果此次变更不符合预期，可能有人在冒充该主机。仅在已核实新指纹后才继续。\n信任新证书并继续？`,
    };
  }
  lines.push(`指纹（SHA-256）：${status.fingerprint}`);
  return {
    title: "确认服务器证书",
    body: `${lines.join("\n")}\n\n首次连接该主机。信任并继续？指纹将被记住，之后证书变化时会再次提示。`,
  };
}
