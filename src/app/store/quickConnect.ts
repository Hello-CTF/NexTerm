import { assetApi, sessionApi, vaultApi } from "../../ipc/commands";
import { DESKTOP } from "../../ipc/env";
import { describeError } from "../../ui/errorText";
import { connectWithHostKeyConfirm } from "../hostKeys";
import { formatQuickConnectTarget, type QuickConnectTarget } from "../quickConnectTarget";
import {
  captureConnectionAttempt,
  connectAsset,
  currentConnectionEpoch,
  discardStaleSession,
  ensureVaultUnlocked,
  openConnectedAssetSession,
  settleQuickConnectionOutcome,
} from "./connection";
import { nextTabId } from "./model";
import { assetConnectionProblemKey, quickConnectionProblemKey } from "./problems";
import type { ConnectAssetOptions, ConnectOutcome, SaveQuickConnectOutcome } from "./types";
import { useUi } from "./useUi";

const inflightQuickConnects = new Map<string, Promise<ConnectOutcome>>();

export async function promptQuickConnectPassword(display: string): Promise<string | null> {
  const { promptText } = await import("../../ui/dialogs");
  return promptText(`输入 ${display} 的登录密码（留空则使用 ssh-agent）`, "", {
    secret: true,
  });
}

export async function connectQuickTarget(
  target: QuickConnectTarget,
  options?: ConnectAssetOptions,
): Promise<ConnectOutcome> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const key = JSON.stringify([epoch, quickConnectionProblemKey(target)]);
  const inflight = inflightQuickConnects.get(key);
  if (inflight) return inflight;
  const context = captureConnectionAttempt();
  const promise = runQuickConnectTarget(target, options, epoch).finally(() => {
    if (inflightQuickConnects.get(key) === promise) inflightQuickConnects.delete(key);
  });
  inflightQuickConnects.set(key, promise);
  const outcome = await promise;
  if (currentConnectionEpoch(epoch)) settleQuickConnectionOutcome(context, target, outcome);
  return outcome;
}

async function runQuickConnectTarget(
  target: QuickConnectTarget,
  options?: ConnectAssetOptions,
  epoch = useUi.getState().workspaceRuntimeEpoch,
): Promise<ConnectOutcome> {
  if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
  const { pushToast } = useUi.getState();
  const display = formatQuickConnectTarget(target);
  const password = await promptQuickConnectPassword(display);
  if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
  if (password === null) return { ok: false, canceled: true };
  try {
    const info = await connectWithHostKeyConfirm(() =>
      sessionApi.connectQuick({
        host: target.host,
        port: target.port,
        ...(target.username ? { username: target.username } : {}),
        authKind: password ? "password" : "agent",
        ...(password ? { password } : {}),
      }),
      () => currentConnectionEpoch(epoch),
    );
    if (!currentConnectionEpoch(epoch)) return info ? discardStaleSession(info) : { ok: false, canceled: true };
    if (!info) {
      pushToast("info", "已取消连接");
      return { ok: false, canceled: true };
    }
    await openConnectedAssetSession(info, { id: "", name: info.name, kind: "ssh" }, options, {
      id: nextTabId("quick-target"),
      host: target.host,
      port: target.port,
      username: target.username,
      authKind: password ? "password" : "agent",
    });
    return { ok: true };
  } catch (e) {
    if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
    const err = e as { message?: string };
    const message = err.message || describeError(e);
    if (!options?.silent) pushToast("error", message);
    return { ok: false, error: message };
  }
}

interface SaveQuickConnectAttempt {
  outcome: ConnectOutcome;
  asset?: SaveQuickConnectOutcome["asset"];
}

export async function saveQuickConnectAsset(
  target: QuickConnectTarget,
  options?: ConnectAssetOptions,
): Promise<SaveQuickConnectOutcome> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const context = captureConnectionAttempt();
  const quickKey = quickConnectionProblemKey(target);
  const attempt = await runSaveQuickConnectAsset(target, options, epoch);
  if (currentConnectionEpoch(epoch) && !attempt.asset) {
    settleQuickConnectionOutcome(context, target, attempt.outcome, "save");
  }
  return {
    ...attempt.outcome,
    ...(attempt.asset ? { asset: attempt.asset } : {}),
    problemKey: attempt.asset ? assetConnectionProblemKey(attempt.asset.id) : quickKey,
  };
}

async function runSaveQuickConnectAsset(
  target: QuickConnectTarget,
  options: ConnectAssetOptions | undefined,
  epoch: number,
): Promise<SaveQuickConnectAttempt> {
  const { pushToast } = useUi.getState();
  const display = formatQuickConnectTarget(target);
  let created: SaveQuickConnectOutcome["asset"];
  const finish = (outcome: ConnectOutcome): SaveQuickConnectAttempt => ({
    outcome,
    ...(created ? { asset: created } : {}),
  });
  const password = await promptQuickConnectPassword(display);
  if (!currentConnectionEpoch(epoch)) return finish({ ok: false, canceled: true });
  if (password === null) return finish({ ok: false, canceled: true });
  try {
    // The server assembly disabled session_quick_connect_user: in web mode, do not backfill the server OS user.
    const username = target.username ?? (DESKTOP ? await sessionApi.quickConnectDefaultUser() : "");
    if (!currentConnectionEpoch(epoch)) return finish({ ok: false, canceled: true });
    let credId: string | null = null;
    if (password) {
      if (!(await ensureVaultUnlocked("保存凭据前需要解锁凭据库", epoch)) || !currentConnectionEpoch(epoch)) {
        return finish({ ok: false, canceled: true });
      }
      const res = await vaultApi.setCredential(display, "password", password);
      credId = res.id;
      if (!currentConnectionEpoch(epoch)) return finish({ ok: false, canceled: true });
    }
    try {
      created = await assetApi.create({
        kind: "ssh",
        name: display,
        host: target.host,
        port: target.port,
        username,
        authKind: password ? "password" : "agent",
        keyPath: null,
        credId,
      });
      if (!currentConnectionEpoch(epoch)) return finish({ ok: false, canceled: true });
    } catch (e) {
      if (credId && currentConnectionEpoch(epoch)) await vaultApi.deleteCredential(credId).catch(() => undefined);
      throw e;
    }
    useUi.getState().clearConnectionProblem(quickConnectionProblemKey(target));
    return finish(await connectAsset(created, { ...options, openTerminal: true }));
  } catch (e) {
    if (!currentConnectionEpoch(epoch)) return finish({ ok: false, canceled: true });
    const err = e as { message?: string };
    const message = err.message || describeError(e);
    if (!options?.silent) pushToast("error", message);
    return finish({ ok: false, error: message });
  }
}
