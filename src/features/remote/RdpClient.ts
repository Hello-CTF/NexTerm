import type { IronError, UserInteraction } from "@devolutions/iron-remote-desktop";
import { prepareRdpTransport, remoteProxyAddress } from "./remoteSocket";
import { RemoteEngineError, type ClientCallbacks, type ClientOptions, type EngineFailure, type RemoteClient, type RemoteSecret, type ScaleMode } from "./remoteTypes";
import { drawThumbnail } from "./thumbnail";

type IronElement = HTMLElement & { module?: unknown };

// The component sizes its own canvas against the window. These rules take that away so the pane controls the size:
// the wrapper no longer clips or shrinks, and the component's own focus outline gives way to the pane's border.
const OVERRIDE_CSS = `
  .screen-wrapper { height: auto !important; width: auto !important; overflow: visible !important; }
  .capturing-inputs { outline: none !important; }
`;

const KEY_NAMES: Record<string, string> = {
  ControlLeft: "Control",
  AltLeft: "Alt",
  ShiftLeft: "Shift",
  MetaLeft: "Meta",
  Escape: "Escape",
  Tab: "Tab",
  Delete: "Delete",
  Enter: "Enter",
  Backspace: "Backspace",
  PrintScreen: "PrintScreen",
  Space: " ",
};

function keyNameOf(code: string): string {
  const named = KEY_NAMES[code];
  if (named) return named;
  if (code.startsWith("Key")) return code.slice(3).toLowerCase();
  if (code.startsWith("Digit")) return code.slice(5);
  return code;
}

function engineFailureOf(error: unknown): EngineFailure {
  const iron = error as Partial<IronError> | null;
  let rdpKind: number | undefined;
  let httpStatus: number | undefined;
  let tlsAlert: number | undefined;
  let message = "";
  try {
    rdpKind = typeof iron?.kind === "function" ? Number(iron.kind()) : undefined;
    const details = typeof iron?.rdcleanpathDetails === "function" ? iron.rdcleanpathDetails() : undefined;
    httpStatus = details?.httpStatusCode;
    tlsAlert = details?.tlsAlertCode;
    message = typeof iron?.backtrace === "function" ? iron.backtrace().split("\n")[0].trim() : "";
  } catch {
    // A malformed error object is reported as a plain failure.
  }
  if (!message) message = error instanceof Error ? error.message : "RDP 连接失败";
  return { protocol: "rdp", stage: "connect", message: message.slice(0, 200), rdpKind, httpStatus, tlsAlert };
}

// RdpClient drives the IronRDP web component (WebAssembly, loaded on first use). Audio redirection is not part of the
// component's API, so a session has no sound.
export class RdpClient implements RemoteClient {
  readonly protocol = "rdp" as const;
  readonly supportsResize = true;
  private element: IronElement | null = null;
  private ui: UserInteraction | null = null;
  private sizer: HTMLElement | null = null;
  private holder: HTMLElement | null = null;
  private observer: ResizeObserver | null = null;
  private scale: ScaleMode = "fit";
  private connected = false;
  private closed = false;
  private viewOnly: boolean;
  // The login secret lives here only while connecting; it is dropped as soon as the handshake has used it.
  private secret: RemoteSecret | null;
  private focusAllowed = false;
  private readonly held = new Set<string>();

  private readonly options: ClientOptions;

  constructor(
    private readonly stage: HTMLElement,
    options: ClientOptions,
    private readonly callbacks: ClientCallbacks,
  ) {
    this.viewOnly = options.viewOnly;
    this.secret = options.secret;
    // The stored options never hold the secret, so a later read of them cannot leak it.
    this.options = { ...options, secret: null };
  }

  async connect(): Promise<void> {
    try {
      await prepareRdpTransport();
      await import("@devolutions/iron-remote-desktop");
      const rdp = await import("@devolutions/iron-remote-desktop-rdp");
      await rdp.init("WARN");
      if (this.closed) throw new RemoteEngineError({ protocol: "rdp", stage: "closed", clean: true, message: "已取消" });
      const ui = await this.mount(rdp.Backend);
      this.ui = ui;
      if (this.closed) throw new RemoteEngineError({ protocol: "rdp", stage: "closed", clean: true, message: "已取消" });
      ui.setVisibility(true);
      ui.setScale(3);
      ui.setEnableClipboard(this.options.clipboard);
      ui.setEnableAutoClipboard(false);
      ui.onWarningCallback((message) => console.warn("rdp:", message));
      const secret = this.secret;
      const prepared = this.options.prepared;
      const builder = ui
        .configBuilder()
        .withUsername(secret?.username ?? prepared.username)
        .withPassword(secret?.password ?? "")
        .withDestination(`${prepared.host}:${prepared.port}`)
        .withProxyAddress(remoteProxyAddress())
        .withAuthToken(prepared.ticket)
        .withServerDomain(secret?.domain ?? prepared.domain)
        .withExtension(rdp.displayControl(true));
      if (this.options.initialSize) builder.withDesktopSize(this.options.initialSize);
      let session;
      try {
        session = await ui.connect(builder.build());
      } finally {
        this.secret = null;
      }
      if (this.closed) throw new RemoteEngineError({ protocol: "rdp", stage: "closed", clean: true, message: "已取消" });
      this.connected = true;
      this.guardFocus();
      this.layout();
      this.callbacks.onConnected();
      const size = this.desktopSize();
      this.callbacks.onDesktopSize?.(size.width, size.height);
      void session
        .run()
        .then((info) => {
          if (this.closed) return;
          this.connected = false;
          this.callbacks.onDisconnected({ protocol: "rdp", stage: "closed", clean: true, wasConnected: true, message: info.reason() || "远程桌面已断开" });
        })
        .catch((error: unknown) => {
          if (this.closed) return;
          this.connected = false;
          this.callbacks.onDisconnected({ ...engineFailureOf(error), stage: "closed", wasConnected: true });
        });
    } catch (error) {
      this.secret = null;
      this.teardown();
      if (error instanceof RemoteEngineError) throw error;
      throw new RemoteEngineError(engineFailureOf(error));
    }
  }

  private mount(backend: unknown): Promise<UserInteraction> {
    return new Promise<UserInteraction>((resolve, reject) => {
      const element = document.createElement("iron-remote-desktop") as IronElement;
      element.setAttribute("scale", "real");
      element.setAttribute("verbose", "false");
      element.setAttribute("flexcenter", "false");
      element.module = backend;
      element.style.display = "block";
      const timer = window.setTimeout(() => reject(new Error("远程桌面组件加载超时")), 15000);
      element.addEventListener(
        "ready",
        (event) => {
          window.clearTimeout(timer);
          resolve((event as CustomEvent<{ irgUserInteraction: UserInteraction }>).detail.irgUserInteraction);
        },
        { once: true },
      );
      const sizer = document.createElement("div");
      const holder = document.createElement("div");
      sizer.style.cssText = "position:absolute;left:0;top:0";
      holder.style.cssText = "transform-origin:0 0;position:absolute;left:0;top:0";
      holder.appendChild(element);
      sizer.appendChild(holder);
      this.stage.style.overflow = "auto";
      this.stage.appendChild(sizer);
      this.sizer = sizer;
      this.holder = holder;
      this.element = element;
      try {
        const sheet = new CSSStyleSheet();
        sheet.replaceSync(OVERRIDE_CSS);
        element.shadowRoot?.adoptedStyleSheets?.push(sheet);
      } catch {
        // Constructable stylesheets are missing on very old WebViews; the component then sizes itself.
      }
      this.observer = new ResizeObserver(() => this.layout());
      this.observer.observe(this.stage);
      this.observer.observe(element);
    });
  }

  private canvas(): HTMLCanvasElement | null {
    return this.element?.shadowRoot?.querySelector("canvas") ?? null;
  }

  // The component focuses its canvas whenever the pointer merely passes over it. The pane wants focus on a click only
  // (so the keyboard is not captured by a hover), so the canvas's own focus() answers only while a click is handled.
  private guardFocus(): void {
    const canvas = this.canvas();
    if (!canvas) return;
    const original = canvas.focus.bind(canvas);
    canvas.focus = (options?: FocusOptions) => {
      if (this.focusAllowed) original(options);
    };
  }

  private layout(): void {
    const canvas = this.canvas();
    const sizer = this.sizer;
    const holder = this.holder;
    if (!canvas || !sizer || !holder || canvas.width < 2) return;
    const dw = canvas.width;
    const dh = canvas.height;
    const cw = this.stage.clientWidth;
    const ch = this.stage.clientHeight;
    if (cw < 2 || ch < 2) return;
    const scale = this.scale === "fit" ? Math.min(cw / dw, ch / dh) : this.scale;
    const w = dw * scale;
    const h = dh * scale;
    sizer.style.width = `${w}px`;
    sizer.style.height = `${h}px`;
    sizer.style.left = `${Math.max(0, (cw - w) / 2)}px`;
    sizer.style.top = `${Math.max(0, (ch - h) / 2)}px`;
    holder.style.width = `${dw}px`;
    holder.style.height = `${dh}px`;
    holder.style.transform = `scale(${scale})`;
  }

  disconnect(): void {
    this.closed = true;
    this.releaseAllKeys();
    this.teardown();
  }

  private teardown(): void {
    this.observer?.disconnect();
    this.observer = null;
    try {
      this.ui?.shutdown();
    } catch {
      // The session may already be gone.
    }
    this.ui = null;
    this.sizer?.remove();
    this.sizer = null;
    this.holder = null;
    this.element = null;
    this.connected = false;
  }

  setScale(mode: ScaleMode): void {
    this.scale = mode;
    this.layout();
  }

  setViewOnly(viewOnly: boolean): void {
    this.viewOnly = viewOnly;
    if (viewOnly) this.releaseAllKeys();
  }

  resize(width: number, height: number): void {
    if (!this.ui || !this.connected) return;
    const w = Math.max(320, Math.min(8192, Math.floor(width) & ~1));
    const h = Math.max(240, Math.min(8192, Math.floor(height) & ~1));
    try {
      this.ui.resize(w, h);
    } catch {
      // The server may not allow a resize right now; the next layout pass tries again.
    }
  }

  focus(): void {
    this.focusAllowed = true;
    try {
      this.canvas()?.focus({ preventScroll: true });
    } finally {
      this.focusAllowed = false;
    }
  }

  blur(): void {
    // Keys still down on the remote, the release chord's Ctrl+Alt+Shift among them, must be let go before the
    // component stops reading the keyboard: it drops their key-up events once the canvas lost focus.
    this.releaseSessionInputs();
    this.canvas()?.blur();
  }

  // ui.connect() hands back no Session object, so the component's own release is used: its canvas calls
  // Session.releaseAllInputs() when the pointer leaves, and a synthetic mouseleave reaches exactly that handler.
  private releaseSessionInputs(): void {
    this.canvas()?.dispatchEvent(new MouseEvent("mouseleave", { bubbles: false }));
  }

  private hasFocus(): boolean {
    return this.element !== null && document.activeElement === this.element;
  }

  private dispatchKey(code: string, down: boolean): void {
    const init: KeyboardEventInit = {
      code,
      key: keyNameOf(code),
      bubbles: true,
      cancelable: true,
      composed: true,
      ctrlKey: this.held.has("ControlLeft"),
      altKey: this.held.has("AltLeft"),
      shiftKey: this.held.has("ShiftLeft"),
      metaKey: this.held.has("MetaLeft"),
    };
    window.dispatchEvent(new KeyboardEvent(down ? "keydown" : "keyup", init));
  }

  sendKey(code: string, down: boolean): void {
    if (this.viewOnly || !this.connected) return;
    // The component only reads the keyboard while its canvas has focus.
    if (!this.hasFocus()) this.focus();
    if (down) this.held.add(code);
    else this.held.delete(code);
    this.dispatchKey(code, down);
  }

  async sendCombo(codes: readonly string[]): Promise<void> {
    if (this.viewOnly || !this.connected) return;
    const step = () => new Promise<void>((resolve) => window.setTimeout(resolve, 15));
    for (const code of codes) {
      this.sendKey(code, true);
      await step();
    }
    for (const code of [...codes].reverse()) {
      this.sendKey(code, false);
      await step();
    }
  }

  sendText(text: string): void {
    if (this.viewOnly || !this.connected || !this.ui) return;
    if (!this.hasFocus()) this.focus();
    this.ui.setKeyboardUnicodeMode(true);
    try {
      for (const char of text) {
        if (char.length !== 1) continue;
        for (const type of ["keydown", "keyup"]) {
          window.dispatchEvent(new KeyboardEvent(type, { key: char, code: "", bubbles: true, cancelable: true, composed: true }));
        }
      }
    } finally {
      this.ui.setKeyboardUnicodeMode(false);
    }
  }

  releaseAllKeys(): void {
    for (const code of [...this.held]) this.dispatchKey(code, false);
    this.held.clear();
    this.releaseSessionInputs();
  }

  async sendClipboard(): Promise<boolean> {
    if (!this.options.clipboard || !this.ui || this.viewOnly) return false;
    try {
      await this.ui.sendClipboardData();
      return true;
    } catch {
      return false;
    }
  }

  async fetchClipboard(): Promise<boolean> {
    if (!this.options.clipboard || !this.ui) return false;
    try {
      await this.ui.saveRemoteClipboardData();
      return true;
    } catch {
      return false;
    }
  }

  inputElement(): HTMLElement | null {
    return this.canvas();
  }

  thumbnail(maxWidth: number): string | null {
    return drawThumbnail(this.canvas(), maxWidth);
  }

  desktopSize(): { width: number; height: number } {
    const canvas = this.canvas();
    return { width: canvas?.width ?? 0, height: canvas?.height ?? 0 };
  }
}

export async function createRdpClient(
  stage: HTMLElement,
  options: ClientOptions,
  callbacks: ClientCallbacks,
): Promise<RemoteClient> {
  // Built but not connected: the caller registers the client first, so a close during connect can reach it.
  return new RdpClient(stage, options, callbacks);
}
