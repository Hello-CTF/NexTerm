import { openVncChannel, type PreambleChannel } from "./remoteSocket";
import { keysymOf } from "./keys";
import {
  RemoteEngineError,
  type ClientCallbacks,
  type ClientOptions,
  type EngineFailure,
  type RemoteClient,
  type ScaleMode,
} from "./remoteTypes";
import { drawThumbnail } from "./thumbnail";

type RfbCtor = typeof import("@novnc/novnc").default;
type RfbInstance = InstanceType<RfbCtor>;

// The VNC client wraps noVNC (loaded on first use, so it stays out of the entry chunk). The relay performs the VNC
// authentication with the password it holds, so noVNC negotiates "no security" and needs no credentials.
export class VncClient implements RemoteClient {
  readonly protocol = "vnc" as const;
  readonly supportsResize = false;
  private rfb: RfbInstance | null = null;
  private channel: PreambleChannel | null = null;
  private connected = false;
  private closed = false;
  private remoteClipboard = "";
  private foreground = true;
  private scale: ScaleMode = "fit";
  private readonly abort = new AbortController();

  constructor(
    private readonly stage: HTMLElement,
    private readonly options: ClientOptions,
    private readonly callbacks: ClientCallbacks,
  ) {}

  async connect(): Promise<void> {
    const [{ default: RFB }, channel] = await Promise.all([
      import("@novnc/novnc"),
      openVncChannel(this.options.prepared.ticket, this.abort.signal),
    ]).catch((error: unknown) => {
      throw new RemoteEngineError({
        protocol: "vnc",
        stage: "connect",
        message: error instanceof Error ? error.message : "无法连接到远程桌面中继",
      });
    });
    if (this.closed) {
      channel.close();
      throw new RemoteEngineError({ protocol: "vnc", stage: "closed", clean: true, message: "已取消" });
    }
    this.channel = channel;
    return new Promise<void>((resolve, reject) => {
      const rfb = new RFB(this.stage, channel, { shared: true });
      this.rfb = rfb;
      rfb.focusOnClick = true;
      rfb.background = "transparent";
      rfb.showDotCursor = true;
      rfb.viewOnly = this.options.viewOnly;
      this.applyScale();
      let settled = false;
      rfb.addEventListener("connect", () => {
        this.connected = true;
        settled = true;
        this.callbacks.onConnected();
        resolve();
      });
      rfb.addEventListener("securityfailure", (event) => {
        const detail = (event as CustomEvent<{ reason?: string; status?: number }>).detail;
        const failure: EngineFailure = { protocol: "vnc", stage: "security", message: detail?.reason ?? "VNC 安全协商失败" };
        if (!settled) {
          settled = true;
          reject(new RemoteEngineError(failure));
        }
      });
      rfb.addEventListener("disconnect", (event) => {
        const clean = (event as CustomEvent<{ clean?: boolean }>).detail?.clean === true;
        this.rfb = null;
        if (!settled) {
          settled = true;
          reject(new RemoteEngineError({ protocol: "vnc", stage: "closed", clean, wasConnected: false, message: "连接已关闭" }));
          return;
        }
        // A session that never reached the connected state was already reported by the rejected connect().
        if (!this.closed && this.connected) {
          this.callbacks.onDisconnected({
            protocol: "vnc",
            stage: "closed",
            // noVNC calls any socket close of an established session clean, a dropped network included. A close this
            // client did not ask for is a drop, so it is retried.
            clean: false,
            wasConnected: this.connected,
            message: "与远程桌面的连接已中断",
          });
        }
      });
      rfb.addEventListener("clipboard", (event) => {
        this.remoteClipboard = (event as CustomEvent<{ text: string }>).detail.text;
        this.callbacks.onRemoteClipboard?.(this.remoteClipboard);
        if (this.options.clipboard && this.options.clipboardAutoSync && this.foreground && document.hasFocus()) {
          void navigator.clipboard?.writeText(this.remoteClipboard).catch(() => undefined);
        }
      });
      rfb.addEventListener("desktopsize" as string, () => {
        const size = this.desktopSize();
        this.callbacks.onDesktopSize?.(size.width, size.height);
      });
    });
  }

  disconnect(): void {
    this.closed = true;
    this.abort.abort();
    try {
      this.rfb?.disconnect();
    } catch {
      // Already gone.
    }
    this.channel?.close();
    this.rfb = null;
  }

  private applyScale(): void {
    const rfb = this.rfb;
    if (!rfb) return;
    if (this.scale === "fit") {
      rfb.clipViewport = false;
      rfb.scaleViewport = true;
      rfb.dragViewport = false;
    } else {
      // noVNC has no arbitrary zoom: anything but fit shows the picture at its own size and scrolls.
      rfb.scaleViewport = false;
      rfb.clipViewport = true;
      rfb.dragViewport = true;
    }
  }

  setScale(mode: ScaleMode): void {
    this.scale = mode;
    this.applyScale();
  }

  setViewOnly(viewOnly: boolean): void {
    if (this.rfb) this.rfb.viewOnly = viewOnly;
  }

  resize(): "unavailable" {
    // The relay offers no desktop resize for VNC; the server decides the size.
    return "unavailable";
  }

  focus(): void {
    this.rfb?.focus({ preventScroll: true });
  }

  blur(): void {
    this.rfb?.blur();
  }

  sendKey(code: string, down: boolean): void {
    const keysym = keysymOf(code);
    if (keysym === null || !this.rfb || this.rfb.viewOnly) return;
    this.rfb.sendKey(keysym, code, down);
  }

  async sendCombo(codes: readonly string[]): Promise<void> {
    if (codes.length === 3 && codes[0] === "ControlLeft" && codes[1] === "AltLeft" && codes[2] === "Delete" && this.rfb && !this.rfb.viewOnly) {
      this.rfb.sendCtrlAltDel();
      return;
    }
    for (const code of codes) this.sendKey(code, true);
    for (const code of [...codes].reverse()) this.sendKey(code, false);
  }

  sendText(text: string): void {
    if (!this.rfb || this.rfb.viewOnly) return;
    for (const char of text) {
      const point = char.codePointAt(0) ?? 0;
      // X11 keysyms for Unicode characters beyond Latin-1 are 0x01000000 + the code point.
      const keysym = point < 0x100 ? point : 0x01000000 + point;
      this.rfb.sendKey(keysym, null, true);
      this.rfb.sendKey(keysym, null, false);
    }
  }

  releaseAllKeys(): void {
    // noVNC releases its own held keys on blur; the modifiers this app latched are released by the pane.
  }

  async sendClipboard(): Promise<boolean> {
    if (!this.options.clipboard || !this.rfb || this.rfb.viewOnly) return false;
    try {
      const text = await navigator.clipboard.readText();
      if (!text) return false;
      this.rfb.clipboardPasteFrom(text);
      return true;
    } catch {
      return false;
    }
  }

  setForeground(foreground: boolean): void {
    this.foreground = foreground;
  }

  async fetchClipboard(): Promise<boolean> {
    if (!this.options.clipboard || !this.remoteClipboard) return false;
    try {
      await navigator.clipboard.writeText(this.remoteClipboard);
      return true;
    } catch {
      return false;
    }
  }

  inputElement(): HTMLElement | null {
    return this.stage.querySelector("canvas");
  }

  thumbnail(maxWidth: number): string | null {
    return drawThumbnail(this.stage.querySelector("canvas"), maxWidth);
  }

  desktopSize(): { width: number; height: number } {
    const canvas = this.stage.querySelector("canvas");
    return { width: canvas?.width ?? 0, height: canvas?.height ?? 0 };
  }
}

export async function createVncClient(
  stage: HTMLElement,
  options: ClientOptions,
  callbacks: ClientCallbacks,
): Promise<RemoteClient> {
  // Built but not connected: the caller registers the client first, so a close during connect can reach it.
  return new VncClient(stage, options, callbacks);
}
