import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { assetApi, remoteDesktopApi } from "../../ipc/commands";
import { ask, promptText } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { releaseKeyboard } from "../../app/inputCapture";
import {
  classifyFailure,
  describeCertificate,
  mayAutoReconnect,
  retryDelayMs,
  runConnectFlow,
  type Failure,
  type FlowApi,
  type FlowUi,
} from "./connectFlow";
import { clearRemoteStatus, setRemoteStatus, setRemoteThumbnail, THUMBNAIL_INTERVAL_MS, THUMBNAIL_WIDTH, thumbnailsWanted } from "./remoteStatus";
import { readRemoteDesktopOptions, type RemoteDesktopOptions } from "../explorer/assetKindFields";
import { RemoteEngineError, type ClientCallbacks, type ClientFactory, type EngineFailure, type RemoteClient, type RemoteProtocol, type RemoteStatus } from "./remoteTypes";

const flowApi: FlowApi = {
  prepare: (assetId) => remoteDesktopApi.prepare(assetId),
  certificate: (assetId) => remoteDesktopApi.certificate(assetId),
  trust: (assetId, fingerprint) => remoteDesktopApi.trustCertificate(assetId, fingerprint),
  setPassword: (ticket, password) => remoteDesktopApi.setPassword(ticket, password),
  releaseSecret: (ticket) => remoteDesktopApi.releaseSecret(ticket),
};

const flowUi: FlowUi = {
  askPassword: (prepared) =>
    promptText(`输入「${prepared.name}」的登录密码${prepared.username ? `（${prepared.username}）` : ""}\n密码只用于本次连接，不会被保存。`, "", {
      secret: true,
    }),
  confirmCertificate: (status) => {
    const text = describeCertificate(status);
    return ask(text.body, { title: text.title, kind: "warning" });
  },
};

async function factoryFor(protocol: RemoteProtocol): Promise<ClientFactory> {
  if (protocol === "vnc") return (await import("./VncClient")).createVncClient;
  return (await import("./RdpClient")).createRdpClient;
}

export interface RemoteSession {
  status: RemoteStatus;
  failure: Failure | null;
  // Seconds until the next automatic reconnect, or null when none is scheduled.
  retryIn: number | null;
  attempt: number;
  protocol: RemoteProtocol;
  options: RemoteDesktopOptions;
  clientRef: RefObject<RemoteClient | null>;
  connect: () => void;
  disconnect: () => void;
}

const DEFAULT_OPTIONS = readRemoteDesktopOptions("", undefined);

// useRemoteSession owns one connection of one remote desktop tab: the connect flow, the live client, the automatic
// reconnect after a network drop, and the status the overview reads. It connects when the pane mounts; a restored
// workspace never mounts the pane until the user reconnects it, so nothing here dials on its own after a reload.
export function useRemoteSession(tabId: string, assetId: string, viewportRef: RefObject<HTMLElement | null>): RemoteSession {
  const [status, setStatus] = useState<RemoteStatus>("connecting");
  const [failure, setFailure] = useState<Failure | null>(null);
  const [retryIn, setRetryIn] = useState<number | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [protocol, setProtocol] = useState<RemoteProtocol>("rdp");
  const [options, setOptions] = useState<RemoteDesktopOptions>(DEFAULT_OPTIONS);
  const clientRef = useRef<RemoteClient | null>(null);
  const holderRef = useRef<HTMLElement | null>(null);
  const generation = useRef(0);
  const attemptRef = useRef(0);
  const timers = useRef<{ countdown: number | null; retry: number | null }>({ countdown: null, retry: null });
  const connectRef = useRef<() => void>(() => undefined);
  // Whether the flow behind the live client asked the user for a password; such a session is not reconnected by
  // itself, because nobody is there to type it again.
  const promptedRef = useRef(false);

  const clearTimers = useCallback(() => {
    if (timers.current.countdown !== null) window.clearInterval(timers.current.countdown);
    if (timers.current.retry !== null) window.clearTimeout(timers.current.retry);
    timers.current = { countdown: null, retry: null };
    setRetryIn(null);
  }, []);

  const dropClient = useCallback(() => {
    const client = clientRef.current;
    clientRef.current = null;
    try {
      client?.disconnect();
    } catch {
      // The connection is gone either way.
    }
    holderRef.current?.remove();
    holderRef.current = null;
  }, []);

  const settle = useCallback(
    (next: RemoteStatus) => {
      setStatus(next);
      setRemoteStatus(tabId, next);
    },
    [tabId],
  );

  const fail = useCallback(
    (result: Failure, promptPassword: boolean, gen: number) => {
      if (gen !== generation.current) return;
      const last = clientRef.current?.thumbnail(THUMBNAIL_WIDTH);
      if (last) setRemoteThumbnail(tabId, last);
      setFailure(result);
      settle("disconnected");
      if (!mayAutoReconnect(result, { promptPassword, attempt: attemptRef.current })) return;
      const delay = retryDelayMs(attemptRef.current);
      if (delay === null) return;
      let remaining = Math.ceil(delay / 1000);
      setRetryIn(remaining);
      timers.current.countdown = window.setInterval(() => {
        remaining -= 1;
        setRetryIn(Math.max(0, remaining));
      }, 1000);
      timers.current.retry = window.setTimeout(() => {
        clearTimers();
        attemptRef.current += 1;
        setAttempt(attemptRef.current);
        connectRef.current();
      }, delay);
    },
    [clearTimers, settle, tabId],
  );

  const connect = useCallback(() => {
    const gen = ++generation.current;
    clearTimers();
    dropClient();
    setFailure(null);
    settle("connecting");
    const viewport = viewportRef.current;
    if (!viewport) return;
    const holder = document.createElement("div");
    holder.style.cssText = "position:absolute;inset:0";
    viewport.appendChild(holder);
    holderRef.current = holder;
    void (async () => {
      let asset;
      try {
        asset = await assetApi.get(assetId);
      } catch (error) {
        fail({ kind: "fatal", message: describeError(error) }, false, gen);
        return;
      }
      if (gen !== generation.current) return;
      const parsed = readRemoteDesktopOptions(asset.kind, asset.options);
      setOptions(parsed);
      const kind: RemoteProtocol = asset.kind === "vnc" ? "vnc" : "rdp";
      setProtocol(kind);
      const callbacks: ClientCallbacks = {
        onConnected: () => {
          if (gen !== generation.current) return;
          attemptRef.current = 0;
          setAttempt(0);
          settle("connected");
        },
        onDisconnected: (raw: EngineFailure | null) => {
          if (gen !== generation.current) return;
          fail(classifyFailure(raw ?? { protocol: kind, stage: "closed", wasConnected: true, message: "连接已中断" }), promptedRef.current, gen);
        },
      };
      const result = await runConnectFlow(
        {
          api: flowApi,
          ui: flowUi,
          cancelled: () => gen !== generation.current,
          open: async (prepared, secret) => {
            // A retry inside the flow (certificate confirmed, new ticket) starts on a clean surface.
            clientRef.current?.disconnect();
            clientRef.current = null;
            holder.replaceChildren();
            const create = await factoryFor(prepared.protocol === "vnc" ? "vnc" : "rdp");
            const width = prepared.width && prepared.height ? prepared.width : Math.max(640, viewport.clientWidth);
            const height = prepared.width && prepared.height ? (prepared.height ?? 0) : Math.max(480, viewport.clientHeight);
            const client = await create(
              holder,
              {
                prepared,
                secret,
                viewOnly: parsed.viewOnly,
                clipboard: parsed.clipboard,
                initialSize: { width, height },
              },
              callbacks,
            );
            if (gen !== generation.current) {
              client.disconnect();
              throw new RemoteEngineError({ protocol: kind, stage: "closed", clean: true, message: "已取消" });
            }
            // Registered before it connects, so closing the tab or reconnecting reaches a connect in flight.
            clientRef.current = client;
            await client.connect();
            return client;
          },
        },
        assetId,
      );
      if (gen !== generation.current) {
        if (result.ok) result.client.disconnect();
        return;
      }
      if (result.ok) {
        promptedRef.current = result.prompted;
        clientRef.current = result.client;
        return;
      }
      // A failed attempt leaves no half-open client behind.
      clientRef.current?.disconnect();
      clientRef.current = null;
      fail(result.failure, result.prompted, gen);
    })();
  }, [assetId, clearTimers, dropClient, fail, settle, viewportRef]);
  connectRef.current = connect;

  const manualConnect = useCallback(() => {
    attemptRef.current = 0;
    setAttempt(0);
    connect();
  }, [connect]);

  const disconnect = useCallback(() => {
    generation.current += 1;
    clearTimers();
    dropClient();
    settle("disconnected");
    setFailure({ kind: "ended", message: "已断开连接" });
  }, [clearTimers, dropClient, settle]);

  useEffect(() => {
    connect();
    return () => {
      generation.current += 1;
      clearTimers();
      dropClient();
      clearRemoteStatus(tabId);
      releaseKeyboard(tabId);
    };
    // The connection belongs to the mounted pane: it starts once and ends on unmount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // A low-rate picture for the overview, taken only while the overview asks for one and the page is visible.
  useEffect(() => {
    if (status !== "connected") return;
    const timer = window.setInterval(() => {
      if (!thumbnailsWanted() || document.visibilityState === "hidden") return;
      const picture = clientRef.current?.thumbnail(THUMBNAIL_WIDTH);
      if (picture) setRemoteThumbnail(tabId, picture);
    }, THUMBNAIL_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [status, tabId]);

  return { status, failure, retryIn, attempt, protocol, options, clientRef, connect: manualConnect, disconnect };
}
