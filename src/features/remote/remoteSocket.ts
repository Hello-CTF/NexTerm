import { DESKTOP, wsUrl } from "../../ipc/env";
import { t } from "../../i18n";

// The transport to the remote desktop relay.
//   browser mode: wss://<origin>/ws/remotedesktop, with the ticket in the query (VNC) or in IronRDP's proxy_auth (RDP).
//   desktop and mobile: the Wails stream "remotedesktop", a WebSocket-shaped object over the asset server.

export const REMOTE_DESKTOP_STREAM = "remotedesktop";
export const TICKET_PREAMBLE = "NXRD1:";
// IronRDP opens its own WebSocket from a URL. On desktop and mobile it is given this private address, which the
// WebSocket shim below turns into the Wails stream; the address is never resolved by a network stack.
export const PRIVATE_RDP_PROXY = "nxrd://remotedesktop";

export interface SocketLike {
  readonly readyState: number;
  binaryType: string;
  readonly protocol: string;
  send(data: string | ArrayBufferLike | ArrayBufferView): void;
  close(code?: number, reason?: string): void;
  addEventListener(type: string, listener: (event: Event) => void): void;
  removeEventListener(type: string, listener: (event: Event) => void): void;
}

async function openStream(): Promise<SocketLike> {
  const runtime = await import("@wailsio/runtime");
  return runtime.Stream(REMOTE_DESKTOP_STREAM) as unknown as SocketLike;
}

export function remoteProxyAddress(): string {
  return DESKTOP ? PRIVATE_RDP_PROXY : wsUrl("/ws/remotedesktop");
}

type HandlerName = "open" | "message" | "error" | "close";
type Handler = ((event: Event) => void) | null;

// PreambleChannel is the object handed to noVNC. noVNC checks a channel for send, close, binaryType, onerror,
// onmessage, onopen, protocol and readyState among the keys of the object and the names on its direct prototype,
// which a Wails stream satisfies as it is; this adapter exists because the ticket frame must go out once the socket
// is open and before noVNC takes it over, and because frames that arrive in between must not be lost. It reports
// "open" only after the ticket was sent, so noVNC starts speaking RFB on a relay that already knows the session.
export class PreambleChannel {
  private readonly socket: SocketLike;
  private readonly preamble: string | null;
  private ready = false;
  private readonly handlers: Record<HandlerName, Handler> = { open: null, message: null, error: null, close: null };
  private readonly queued: Array<{ type: HandlerName; event: Event }> = [];
  private readonly readyWaiters: Array<{ resolve: () => void; reject: (error: Error) => void }> = [];
  private ended: Error | null = null;

  constructor(socket: SocketLike, preamble: string | null) {
    this.socket = socket;
    this.preamble = preamble;
    socket.binaryType = "arraybuffer";
    socket.addEventListener("open", () => this.onSocketOpen());
    socket.addEventListener("message", (event) => this.deliver("message", event));
    socket.addEventListener("error", (event) => this.deliver("error", event));
    socket.addEventListener("close", (event) => {
      this.ended = new Error(t("连接已关闭"));
      for (const waiter of this.readyWaiters.splice(0)) waiter.reject(this.ended);
      this.deliver("close", event);
    });
    if (socket.readyState === 1) this.onSocketOpen();
  }

  private onSocketOpen(): void {
    if (this.ready) return;
    try {
      if (this.preamble !== null) this.socket.send(this.preamble);
    } catch {
      this.socket.close();
      return;
    }
    this.ready = true;
    for (const waiter of this.readyWaiters.splice(0)) waiter.resolve();
    this.deliver("open", new Event("open"));
  }

  whenReady(): Promise<void> {
    if (this.ready) return Promise.resolve();
    if (this.ended) return Promise.reject(this.ended);
    return new Promise((resolve, reject) => this.readyWaiters.push({ resolve, reject }));
  }

  private deliver(type: HandlerName, event: Event): void {
    const handler = this.handlers[type];
    if (handler) handler(event);
    else if (type !== "open") this.queued.push({ type, event });
  }

  private setHandler(type: HandlerName, handler: Handler): void {
    this.handlers[type] = handler;
    if (!handler) return;
    // Frames held back are replayed after the code that attached the handler has finished its own set-up: noVNC
    // assigns onmessage first and only then starts its handshake, and a frame handled in between is a protocol error.
    queueMicrotask(() => {
      if (this.handlers[type] !== handler) return;
      for (let index = 0; index < this.queued.length; ) {
        if (this.queued[index].type === type) {
          const [item] = this.queued.splice(index, 1);
          handler(item.event);
        } else {
          index += 1;
        }
      }
    });
  }

  get readyState(): number {
    // The socket may be open while the ticket frame has not gone out yet.
    return this.socket.readyState === 1 && !this.ready ? 0 : this.socket.readyState;
  }
  get protocol(): string {
    return this.socket.protocol;
  }
  get binaryType(): string {
    return this.socket.binaryType;
  }
  set binaryType(value: string) {
    this.socket.binaryType = value;
  }
  get onopen(): Handler {
    return this.handlers.open;
  }
  set onopen(handler: Handler) {
    this.setHandler("open", handler);
  }
  get onmessage(): Handler {
    return this.handlers.message;
  }
  set onmessage(handler: Handler) {
    this.setHandler("message", handler);
  }
  get onerror(): Handler {
    return this.handlers.error;
  }
  set onerror(handler: Handler) {
    this.setHandler("error", handler);
  }
  get onclose(): Handler {
    return this.handlers.close;
  }
  set onclose(handler: Handler) {
    this.setHandler("close", handler);
  }

  send(data: string | ArrayBufferLike | ArrayBufferView): void {
    this.socket.send(data);
  }
  close(code?: number, reason?: string): void {
    this.socket.close(code, reason);
  }
}

// openVncChannel returns a channel that is open and carries the ticket, ready for noVNC.
export async function openVncChannel(ticket: string, signal?: AbortSignal): Promise<PreambleChannel> {
  let socket: SocketLike;
  let preamble: string | null;
  if (DESKTOP) {
    socket = await openStream();
    preamble = `${TICKET_PREAMBLE}${ticket}`;
  } else {
    socket = new WebSocket(wsUrl(`/ws/remotedesktop?ticket=${encodeURIComponent(ticket)}`)) as unknown as SocketLike;
    preamble = null;
  }
  const channel = new PreambleChannel(socket, preamble);
  const abort = () => channel.close();
  signal?.addEventListener("abort", abort, { once: true });
  try {
    await channel.whenReady();
  } finally {
    signal?.removeEventListener("abort", abort);
  }
  return channel;
}

let shimInstalled = false;

// installRdpSocketShim lets IronRDP's WebSocket constructor reach the Wails stream. Only an address that starts with
// the private nxrd: scheme is redirected; every other WebSocket, including the app's own, goes to the original
// constructor untouched. It is installed on desktop and mobile only.
export function installRdpSocketShim(target: { WebSocket: typeof WebSocket } = window, force = false): void {
  if (shimInstalled || (!DESKTOP && !force)) return;
  shimInstalled = true;
  const Native = target.WebSocket;
  target.WebSocket = new Proxy(Native, {
    construct(base, args: unknown[], newTarget) {
      const url = String(args[0]);
      if (url.startsWith("nxrd:")) {
        // The stream returns synchronously with readyState CONNECTING, like new WebSocket(url).
        return lazyStream();
      }
      return Reflect.construct(base, args as [string], newTarget);
    },
  });
}

// The runtime module is already loaded by the time a connection is made, but import() is asynchronous; the shim
// needs a synchronous answer. The RDP client loads the runtime first and stores the constructor here.
let streamFactory: ((name: string) => unknown) | null = null;

export function setStreamFactory(factory: ((name: string) => unknown) | null): void {
  streamFactory = factory;
}

function lazyStream(): object {
  if (!streamFactory) throw new Error(t("远程桌面传输尚未就绪"));
  return streamFactory(REMOTE_DESKTOP_STREAM) as object;
}

export async function prepareRdpTransport(): Promise<void> {
  if (!DESKTOP) return;
  const runtime = await import("@wailsio/runtime");
  setStreamFactory((name) => runtime.Stream(name));
  installRdpSocketShim();
}

export function resetRemoteSocketForTest(): void {
  shimInstalled = false;
  streamFactory = null;
}
