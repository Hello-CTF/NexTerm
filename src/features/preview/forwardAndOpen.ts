import { useUi } from "../../app/store";
import { forwardApi, type ForwardSpec } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { describeError } from "../../ui/errorText";
import { terminalLinkDeps } from "../terminal/terminalLinkActions";
import { openTerminalLink } from "../terminal/terminalLinks";

export interface ForwardTarget {
  sessionId: string;
  host: string;
  port: number;
}

interface ListenerLike {
  address: string;
  port: number;
}

function stripBrackets(address: string): string {
  return address.startsWith("[") && address.endsWith("]") ? address.slice(1, -1) : address;
}

function isV4Reachable(address: string): boolean {
  const a = stripBrackets(address.trim());
  return a === "0.0.0.0" || a === "*" || a === "" || a.startsWith("127.");
}

// The host name to dial from the remote side for a listener bound to `address`. Wildcard and loopback binds are
// reached through loopback; a dual-stack or IPv6-only bind prefers IPv4 when the same port also listens there.
export function targetHostFor(listener: ListenerLike, siblings: readonly ListenerLike[] = []): string {
  const address = stripBrackets(listener.address.trim());
  if (isV4Reachable(address)) return "127.0.0.1";
  if (address === "::" || address === "::1") {
    const hasV4 = siblings.some((s) => s.port === listener.port && isV4Reachable(s.address));
    return hasV4 ? "127.0.0.1" : "::1";
  }
  return listener.address;
}

function sameTarget(spec: ForwardSpec, sessionId: string, host: string, port: number): boolean {
  return (
    spec.temporary === true &&
    spec.sessionId === sessionId &&
    spec.status === "active" &&
    spec.targetHost === host &&
    spec.targetPort === port
  );
}

export async function ensureTemporaryForward(sessionId: string, host: string, port: number): Promise<{ spec: ForwardSpec; reused: boolean }> {
  const existing = (await forwardApi.list()).find((spec) => sameTarget(spec, sessionId, host, port));
  if (existing) return { spec: existing, reused: true };
  return { spec: await forwardApi.createTemporary(sessionId, host, port), reused: false };
}

function isLoopbackHostname(hostname: string): boolean {
  const h = hostname.toLowerCase().replace(/^\[|\]$/g, "");
  return h === "localhost" || h === "::1" || h.startsWith("127.");
}

export async function forwardAndOpen(target: ForwardTarget): Promise<void> {
  const { pushToast } = useUi.getState();
  try {
    const { spec } = await ensureTemporaryForward(target.sessionId, target.host, target.port);
    const address = `127.0.0.1:${spec.listenPort}`;
    const dest = `${target.host.includes(":") ? `[${target.host}]` : target.host}:${target.port}`;
    if (WEB && !isLoopbackHostname(window.location.hostname)) {
      pushToast(
        "info",
        `已在服务器上建立转发 ${address} → ${dest}，浏览器模式下只有服务器本机能访问该地址。会话关闭时自动移除。`,
      );
      return;
    }
    pushToast("success", `已建立转发 ${address} → ${dest}，会话关闭时自动移除。`);
    await openTerminalLink(`http://${address}/`, terminalLinkDeps(target.sessionId, undefined, () => null));
  } catch (error) {
    pushToast("error", `建立转发失败：${describeError(error)}`);
  }
}
