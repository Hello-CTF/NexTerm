import type { IronError, UserInteraction } from "@devolutions/iron-remote-desktop";
import { prepareRdpTransport, remoteProxyAddress } from "./remoteSocket";
import { RemoteEngineError, type ClientCallbacks, type ClipboardOutcome, type ClientOptions, type EngineFailure, type RemoteClient, type RemoteSecret, type ScaleMode } from "./remoteTypes";
import { stripBuildPaths } from "./rdpFailure";
import { drawThumbnail } from "./thumbnail";
import { pasteUploadFiles } from "../terminal/imagePaste";
import { saveRemoteFiles, type RemoteFileEntry } from "./remoteFileSave";
import { copyText, isNativeWebKit, readText } from "../../ui/clipboard";
import { bytesToBase64 } from "../../ui/base64";
import { describeError } from "../../ui/errorText";
import { localFileApi } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { t } from "../../i18n";

type IronElement = HTMLElement & { module?: unknown };

interface WasmClipboardItem {
  mimeType(): string;
  value(): string | Uint8Array;
}

interface WasmClipboardData {
  addText(mimeType: string, text: string): void;
  addBinary(mimeType: string, binary: Uint8Array): void;
  items(): WasmClipboardItem[];
  isEmpty(): boolean;
}

interface WasmSession {
  onClipboardPaste(content: WasmClipboardData): Promise<void>;
  invokeExtension?(ext: unknown): unknown;
  applyInputs?(transaction: WasmInputTransaction): void;
}

interface WasmInputTransaction {
  addEvent(event: unknown): void;
}

interface WasmInputApi {
  InputTransaction: new () => WasmInputTransaction;
  DeviceEvent: { unicodePressed(unicode: string): unknown; unicodeReleased(unicode: string): unknown };
}

interface FileTransferLike {
  getBuilderExtensions(): unknown[];
  setSession(session: unknown): void;
  uploadFiles(files: File[]): unknown;
  on?(event: "files-available", handler: (files: RemoteFileEntry[]) => void): void;
  downloadFile?(file: RemoteFileEntry, index: number): { completion: Promise<Blob> };
  dispose?(): void;
}

// On WebKit, iron-remote-desktop's initClipboard() queries the unsupported "clipboard-read" permission and, in its
// catch block, calls navigator.clipboard.read() — which pops up WebKit's native Paste button and downgrades the
// component to text-only clipboard. Answering "prompt" while a session starts avoids both. The patch is shared by
// every session that is starting, so overlapping starts install and restore it exactly once.
let permissionPatchUsers = 0;
let restorePermissionQuery: (() => void) | null = null;

function patchClipboardPermission(): () => void {
  const permissions = typeof navigator !== "undefined" ? navigator.permissions : undefined;
  if (!isNativeWebKit() || !permissions || typeof permissions.query !== "function") return () => undefined;
  if (permissionPatchUsers === 0) {
    const original = permissions.query;
    const bound = original.bind(permissions);
    try {
      permissions.query = ((desc: PermissionDescriptor) => {
        if ((desc as { name?: string })?.name === "clipboard-read") return Promise.resolve({ state: "prompt" } as PermissionStatus);
        return bound(desc);
      }) as Permissions["query"];
      restorePermissionQuery = () => {
        try {
          permissions.query = original;
        } catch {
          // Read-only permissions object in some environments.
        }
      };
    } catch {
      return () => undefined;
    }
  }
  permissionPatchUsers += 1;
  let released = false;
  return () => {
    if (released) return;
    released = true;
    permissionPatchUsers -= 1;
    if (permissionPatchUsers === 0) {
      restorePermissionQuery?.();
      restorePermissionQuery = null;
    }
  };
}

export async function imageBlobToPngBytes(blob: Blob): Promise<Uint8Array> {
  const raw = new Uint8Array(await blob.arrayBuffer());
  const isPngMagic =
    raw.length >= 8 && raw[0] === 0x89 && raw[1] === 0x50 && raw[2] === 0x4e && raw[3] === 0x47;
  if (blob.type === "image/png" || isPngMagic || typeof createImageBitmap !== "function" || typeof document === "undefined") {
    return raw;
  }
  const bitmap = await createImageBitmap(blob);
  try {
    const canvas = document.createElement("canvas");
    canvas.width = bitmap.width;
    canvas.height = bitmap.height;
    const ctx = canvas.getContext("2d");
    if (ctx) {
      ctx.drawImage(bitmap, 0, 0);
      const pngBlob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/png"));
      if (pngBlob) return new Uint8Array(await pngBlob.arrayBuffer());
    }
  } finally {
    bitmap.close?.();
  }
  return raw;
}

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

// Replaces every non-empty secret with *** so a copied failure report never carries the login or the ticket.
export function redactSecrets(text: string, secrets: readonly string[]): string {
  let out = text;
  for (const secret of secrets) if (secret) out = out.split(secret).join("***");
  return out;
}

export function engineFailureOf(error: unknown, secrets: readonly string[] = []): EngineFailure {
  const iron = error as Partial<IronError> | null;
  let rdpKind: number | undefined;
  let httpStatus: number | undefined;
  let tlsAlert: number | undefined;
  let wsaError: number | undefined;
  let detail = "";
  try {
    rdpKind = typeof iron?.kind === "function" ? Number(iron.kind()) : undefined;
    const details = typeof iron?.rdcleanpathDetails === "function" ? iron.rdcleanpathDetails() : undefined;
    httpStatus = details?.httpStatusCode;
    tlsAlert = details?.tlsAlertCode;
    wsaError = details?.wsaErrorCode;
    detail = typeof iron?.backtrace === "function" ? iron.backtrace().trim() : "";
  } catch {
    // A malformed error object is reported as a plain failure.
  }
  if (!detail) detail = error instanceof Error ? error.message : "";
  detail = redactSecrets(detail, secrets);
  // The first line is the engine's own summary; it is cleaned up for the classifier, never shown as the headline.
  const first = stripBuildPaths(detail.split("\n")[0] ?? "");
  return {
    protocol: "rdp",
    stage: "connect",
    message: (first || t("RDP 连接失败")).slice(0, 200),
    detail: detail.slice(0, 2000),
    rdpKind,
    httpStatus,
    tlsAlert,
    wsaError,
  };
}

// "applied": the canvas reached the requested size, or changed in answer to it (a server may round the size).
// "pending": the Display Control channel is not ready, so sending the request again is right.
// "awaiting": the request was sent and the server has not answered yet; it must not be sent again.
// "unavailable" means no channel; "failed" is another refusal.
export type ResizeOutcome = "applied" | "pending" | "awaiting" | "unavailable" | "failed";

export function resizeOutcomeOf(text: string): ResizeOutcome {
  if (/not yet connected/i.test(text)) return "pending";
  if (/not available/i.test(text)) return "unavailable";
  return "failed";
}

// imageNote adds why the app could not write the image (a missing clipboard tool, say) to a failure note.
function imageNote(note: string, reason: string): string {
  return reason ? t("{note}（{reason}）", { note, reason }) : note;
}

// RdpClient drives the IronRDP web component (WebAssembly, loaded on first use). Audio redirection is not part of the
// component's API, so a session has no sound.
export class RdpClient implements RemoteClient {
  readonly protocol = "rdp" as const;
  readonly supportsResize = true;
  private element: IronElement | null = null;
  private ui: UserInteraction | null = null;
  private wasmSession: WasmSession | null = null;
  private clipboardDataCtor: (new () => WasmClipboardData) | null = null;
  private inputApi: WasmInputApi | null = null;
  private remoteClipboard: WasmClipboardData | null = null;
  // Files the remote side copied last, as its file channel listed them; saved on the toolbar's fetch.
  private remoteFiles: RemoteFileEntry[] | null = null;
  private fileTransfer: FileTransferLike | null = null;
  private sizer: HTMLElement | null = null;
  private holder: HTMLElement | null = null;
  private observer: ResizeObserver | null = null;
  private canvasObserver: MutationObserver | null = null;
  // The size in flight, the canvas size it was sent from, and the canvas size the server answered with (if any).
  // `earlier` holds the sizes of requests this one replaced: their late answers must not satisfy this request.
  private pendingResize: {
    width: number;
    height: number;
    from: { width: number; height: number };
    answer: { width: number; height: number } | null;
    earlier: Array<{ width: number; height: number }>;
  } | null = null;
  private lastDesktopSize = { width: 0, height: 0 };
  private scale: ScaleMode = "fit";
  private connected = false;
  private closed = false;
  private viewOnly: boolean;
  private foreground = true;
  // The login secret lives here only while connecting; it is dropped as soon as the handshake has used it.
  private secret: RemoteSecret | null;
  private focusAllowed = false;
  private lastWarning = "";
  // Values that must never appear in a failure report; kept after the login secret itself is dropped.
  private redact: string[] = [];
  private readonly held = new Set<string>();
  private releasePermissionPatch: (() => void) | null = null;

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
      if (this.closed) throw new RemoteEngineError({ protocol: "rdp", stage: "closed", clean: true, message: t("已取消") });
      this.releasePermissionPatch = patchClipboardPermission();
      const ui = await this.mount(this.wrapBackend(rdp));
      this.ui = ui;
      if (this.closed) throw new RemoteEngineError({ protocol: "rdp", stage: "closed", clean: true, message: t("已取消") });
      ui.setVisibility(true);
      ui.setScale(3);
      ui.setEnableClipboard(this.options.clipboard);
      // The component's own auto mode polls the local clipboard and sends every change; paste sends it instead.
      ui.setEnableAutoClipboard(false);
      ui.onWarningCallback((message) => {
        this.lastWarning = message;
        console.warn("rdp:", message);
      });
      const secret = this.secret;
      const prepared = this.options.prepared;
      this.redact = [secret?.password ?? "", prepared.ticket];
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
        // The component's initClipboard() has run by the time the session is up (or has failed).
        this.releasePermissionPatch?.();
        this.releasePermissionPatch = null;
      }
      if (this.closed) throw new RemoteEngineError({ protocol: "rdp", stage: "closed", clean: true, message: t("已取消") });
      // The handshake is over: the password is no longer held; the ticket stays for later reports.
      this.redact = this.redact.slice(1);
      this.connected = true;
      this.guardFocus();
      const canvas = this.canvas();
      if (canvas) {
        this.canvasObserver = new MutationObserver(() => {
          this.layout();
          this.reportDesktopSize();
        });
        this.canvasObserver.observe(canvas, { attributes: true, attributeFilter: ["width", "height"] });
        const viewer = this.element?.shadowRoot?.querySelector(".screen-viewer");
        if (viewer) this.canvasObserver.observe(viewer, { attributes: true, attributeFilter: ["style"] });
      }
      this.layout();
      this.callbacks.onConnected();
      this.reportDesktopSize();
      void session
        .run()
        .then((info) => {
          if (this.closed) return;
          this.connected = false;
          this.callbacks.onDisconnected({ protocol: "rdp", stage: "closed", clean: true, wasConnected: true, message: redactSecrets(info.reason(), this.redact) || t("远程桌面已断开") });
        })
        .catch((error: unknown) => {
          if (this.closed) return;
          this.connected = false;
          this.callbacks.onDisconnected({ ...engineFailureOf(error, this.redact), stage: "closed", wasConnected: true });
        });
    } catch (error) {
      this.secret = null;
      this.teardown();
      if (error instanceof RemoteEngineError) throw error;
      throw new RemoteEngineError(engineFailureOf(error, this.redact));
    }
  }

  private wrapBackend(rdp: Record<string, unknown>): unknown {
    const rawBackend = rdp.Backend as Record<string, unknown> | undefined;
    const RawSessionBuilder = rawBackend?.SessionBuilder as (new () => Record<string, (...args: unknown[]) => unknown>) | undefined;
    const RawClipboardData = rawBackend?.ClipboardData as (new () => WasmClipboardData) | undefined;
    if (typeof RawSessionBuilder !== "function" || typeof RawClipboardData !== "function") {
      return rawBackend;
    }
    this.clipboardDataCtor = RawClipboardData;
    const RawInputTransaction = rawBackend?.InputTransaction as WasmInputApi["InputTransaction"] | undefined;
    const RawDeviceEvent = rawBackend?.DeviceEvent as Partial<WasmInputApi["DeviceEvent"]> | undefined;
    if (typeof RawInputTransaction === "function" && typeof RawDeviceEvent?.unicodePressed === "function" && typeof RawDeviceEvent.unicodeReleased === "function") {
      this.inputApi = { InputTransaction: RawInputTransaction, DeviceEvent: RawDeviceEvent as WasmInputApi["DeviceEvent"] };
    }
    const FileTransferCtor = rdp.RdpFileTransferProvider as (new () => FileTransferLike) | undefined;
    // eslint-disable-next-line @typescript-eslint/no-this-alias -- needed inside the SessionBuilder constructor function
    const client = this;
    const WrappedSessionBuilder = function () {
      const inner = new RawSessionBuilder();
      const proxy: Record<string, unknown> = new Proxy(inner, {
        get(target, prop) {
          // The component registers its clipboard callbacks from initClipboard(), which runs after its "ready" event
          // and so usually after connect() has built the session: the clipboard channel then never exists. The
          // client owns the clipboard instead and registers its own callback below; the component's are ignored.
          if (prop === "remoteClipboardChangedCallback" || prop === "forceClipboardUpdateCallback") {
            return () => proxy;
          }
          if (prop === "connect") {
            return async () => {
              if (client.options.clipboard) {
                inner.remoteClipboardChangedCallback((data: WasmClipboardData) => client.remoteClipboardChanged(data));
              }
              if (client.options.clipboard && typeof FileTransferCtor === "function") {
                client.fileTransfer?.dispose?.();
                const provider = new FileTransferCtor();
                client.fileTransfer = provider;
                provider.on?.("files-available", (files) => client.remoteFilesAvailable(files));
                for (const ext of provider.getBuilderExtensions()) inner.extension(ext);
              }
              const session = (await inner.connect()) as WasmSession;
              client.wasmSession = session;
              client.fileTransfer?.setSession(session);
              return session;
            };
          }
          const val = Reflect.get(target, prop, target);
          if (typeof val !== "function") return val;
          return (...args: unknown[]) => {
            const out = (val as (...a: unknown[]) => unknown).apply(target, args);
            return out instanceof RawSessionBuilder ? proxy : out;
          };
        },
      });
      return proxy;
    };
    return {
      ...rawBackend,
      SessionBuilder: WrappedSessionBuilder,
    };
  }

  private mount(backend: unknown): Promise<UserInteraction> {
    return new Promise<UserInteraction>((resolve, reject) => {
      const element = document.createElement("iron-remote-desktop") as IronElement;
      element.setAttribute("scale", "real");
      element.setAttribute("verbose", "false");
      element.setAttribute("flexcenter", "false");
      element.module = backend;
      element.style.display = "block";
      const timer = window.setTimeout(() => {
        reject(new Error(t("远程桌面组件加载超时")));
      }, 15000);
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

  private reportDesktopSize(): void {
    const size = this.desktopSize();
    const pending = this.pendingResize;
    if (pending && !pending.answer && this.answersPending(pending, size)) pending.answer = size;
    if (this.lastDesktopSize.width === size.width && this.lastDesktopSize.height === size.height) return;
    this.lastDesktopSize = size;
    this.callbacks.onDesktopSize?.(size.width, size.height);
  }

  private answersPending(pending: NonNullable<RdpClient["pendingResize"]>, size: { width: number; height: number }): boolean {
    if (size.width === pending.from.width && size.height === pending.from.height) return false;
    return !pending.earlier.some((earlier) => earlier.width === size.width && earlier.height === size.height);
  }

  private layout(): void {
    const canvas = this.canvas();
    const sizer = this.sizer;
    const holder = this.holder;
    if (!canvas || !sizer || !holder || canvas.width < 2) return;
    canvas.style.display = "block";
    const dw = canvas.width;
    const dh = canvas.height;
    const cw = this.stage.clientWidth;
    const ch = this.stage.clientHeight;
    if (cw < 2 || ch < 2) return;
    const viewer = this.element?.shadowRoot?.querySelector<HTMLElement>(".screen-viewer");
    if (viewer) {
      const wrongSize =
        viewer.style.width !== `${dw}px` ||
        viewer.style.height !== `${dh}px` ||
        viewer.style.minWidth !== "0px" ||
        viewer.style.minHeight !== "0px" ||
        viewer.style.maxWidth !== "none" ||
        viewer.style.maxHeight !== "none";
      if (wrongSize) {
        viewer.style.width = `${dw}px`;
        viewer.style.height = `${dh}px`;
        viewer.style.minWidth = "0";
        viewer.style.minHeight = "0";
        viewer.style.maxWidth = "none";
        viewer.style.maxHeight = "none";
      }
    }
    if (this.element && (this.element.style.width !== `${dw}px` || this.element.style.height !== `${dh}px`)) {
      this.element.style.width = `${dw}px`;
      this.element.style.height = `${dh}px`;
    }
    this.stage.style.overflow = this.scale === "fit" ? "hidden" : "auto";
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
    this.releasePermissionPatch?.();
    this.releasePermissionPatch = null;
    this.observer?.disconnect();
    this.observer = null;
    this.canvasObserver?.disconnect();
    this.canvasObserver = null;
    this.pendingResize = null;
    this.lastDesktopSize = { width: 0, height: 0 };
    try {
      this.fileTransfer?.dispose?.();
    } catch {
      // Best-effort cleanup.
    }
    this.fileTransfer = null;
    this.wasmSession = null;
    this.clipboardDataCtor = null;
    this.remoteClipboard = null;
    this.remoteFiles = null;
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

  resize(width: number, height: number): ResizeOutcome {
    const w = Math.max(320, Math.min(8192, Math.floor(width) & ~1));
    const h = Math.max(240, Math.min(8192, Math.floor(height) & ~1));
    const size = this.desktopSize();
    if (size.width === w && size.height === h) {
      this.pendingResize = null;
      return "applied";
    }
    if (!this.ui || !this.connected) return "pending";
    const inFlight = this.pendingResize;
    if (inFlight && inFlight.width === w && inFlight.height === h) {
      // A canvas change after the request is the server's answer, even when it rounded or clamped the size.
      if (inFlight.answer && inFlight.answer.width === size.width && inFlight.answer.height === size.height) return "applied";
      if (!inFlight.answer && this.answersPending(inFlight, size)) {
        inFlight.answer = size;
        return "applied";
      }
      if (!inFlight.answer) return "awaiting";
    }
    this.lastWarning = "";
    const earlier = inFlight ? [...inFlight.earlier, { width: inFlight.width, height: inFlight.height }] : [];
    this.pendingResize = { width: w, height: h, from: size, answer: null, earlier };
    try {
      this.ui.resize(w, h);
    } catch (error) {
      this.pendingResize = null;
      const iron = error as Partial<IronError> | null;
      let text = error instanceof Error ? error.message : String(error ?? "");
      try {
        if (typeof iron?.backtrace === "function") text += ` ${iron.backtrace()}`;
      } catch {
        // Fall back to the message alone.
      }
      return resizeOutcomeOf(text);
    }
    // The component may report a refused resize as a warning instead of throwing.
    if (this.lastWarning) {
      this.pendingResize = null;
      return resizeOutcomeOf(this.lastWarning);
    }
    return "awaiting";
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

  sendText(text: string): number {
    if (this.viewOnly || !this.connected || !this.ui) return 0;
    if (!this.hasFocus()) this.focus();
    let skipped = 0;
    this.ui.setKeyboardUnicodeMode(true);
    try {
      for (const char of text) {
        if (char.length === 1) {
          for (const type of ["keydown", "keyup"]) {
            window.dispatchEvent(new KeyboardEvent(type, { key: char, code: "", bubbles: true, cancelable: true, composed: true }));
          }
        } else if (!this.sendUnicodeDirect(char)) {
          skipped += 1;
        }
      }
    } finally {
      this.ui.setKeyboardUnicodeMode(false);
    }
    return skipped;
  }

  // The component only types keys whose name is one UTF-16 code unit, so a character outside the Basic Multilingual
  // Plane (an emoji, say) goes to the session directly; IronRDP sends it as its two surrogate code units.
  private sendUnicodeDirect(char: string): boolean {
    const session = this.wasmSession;
    const api = this.inputApi;
    if (!api || typeof session?.applyInputs !== "function") return false;
    const transaction = new api.InputTransaction();
    transaction.addEvent(api.DeviceEvent.unicodePressed(char));
    transaction.addEvent(api.DeviceEvent.unicodeReleased(char));
    session.applyInputs(transaction);
    return true;
  }

  releaseAllKeys(): void {
    for (const code of [...this.held]) this.dispatchKey(code, false);
    this.held.clear();
    this.releaseSessionInputs();
  }

  private async pasteToSession(fill: (clip: WasmClipboardData) => void): Promise<boolean> {
    if (!this.wasmSession || !this.clipboardDataCtor) return false;
    const clip = new this.clipboardDataCtor();
    fill(clip);
    await this.wasmSession.onClipboardPaste(clip);
    return true;
  }

  private sendDataTransfer(data: DataTransfer): Promise<ClipboardOutcome> {
    const { files, skipped } = pasteUploadFiles(data, { keepImagesWithText: true });
    return this.sendPayload({ files, skipped, plain: data.getData("text/plain"), html: data.getData("text/html") });
  }

  private async sendPayload({ files, skipped, plain, html }: { files: File[]; skipped: string[]; plain: string; html: string }): Promise<ClipboardOutcome> {
    const skippedNote = skipped.length > 0 ? t("未发送：{value}", { value: skipped.join(t("、")) }) : undefined;
    if (files.length > 0) {
      if (files.some((file) => !file.type.startsWith("image/"))) {
        if (!this.fileTransfer) return { status: "unsupported", note: t("此远程桌面会话无法传输文件，文件未发送") };
        this.fileTransfer.uploadFiles(files);
        return { status: "done", note: skippedNote };
      }
      const images = await Promise.all(files.map((file) => imageBlobToPngBytes(file)));
      const sent = await this.pasteToSession((clip) => {
        // A clipboard holds one image per format; the first one goes, the rest are named in the note.
        const first = images[0];
        if (first) clip.addBinary("image/png", first);
        if (plain) clip.addText("text/plain", plain);
      });
      if (!sent) return { status: "refused" };
      const extra = images.length > 1 ? t("只发送了第一张图片，其余 {count} 张未发送", { count: images.length - 1 }) : undefined;
      return { status: "done", note: [extra, skippedNote].filter(Boolean).join(t("；")) || undefined };
    }
    if (!plain && !html) return { status: skipped.length > 0 ? "unsupported" : "empty", note: skippedNote };
    const sent = await this.pasteToSession((clip) => {
      if (plain) clip.addText("text/plain", plain);
      if (html) clip.addText("text/html", html);
    });
    return sent ? { status: "done", note: skippedNote } : { status: "refused" };
  }

  // The remote side announced a new clipboard. It is kept for the toolbar's fetch, and written right away when the
  // user asked for automatic sync and this tab is the one in front.
  private remoteClipboardChanged(data: WasmClipboardData): void {
    if (this.closed) return;
    this.remoteClipboard = data;
    // A file copy is announced with no text or image formats; only a copy that carries content replaces it.
    if (!data.isEmpty()) this.remoteFiles = null;
    if (!this.options.clipboardAutoSync || !this.foreground || !document.hasFocus()) return;
    void this.writeRemoteClipboard().then((outcome) => {
      if (outcome.status === "refused" || outcome.status === "unsupported") {
        this.callbacks.onClipboardNotice?.(outcome.note ?? t("远程剪贴板未能写入本机剪贴板"));
      }
    });
  }

  // The remote side copied files. They cannot go to the local clipboard, so the user is told how to save them.
  private remoteFilesAvailable(files: RemoteFileEntry[]): void {
    if (this.closed) return;
    const count = files.filter((file) => !file.isDirectory).length;
    if (count === 0) return;
    this.remoteFiles = files;
    this.remoteClipboard = null;
    if (!this.foreground || !document.hasFocus()) return;
    this.callbacks.onClipboardNotice?.(t("远程桌面复制了 {count} 个文件，点击工具栏「取回远程剪贴板到本机」保存", { count }));
  }

  private async writeRemoteClipboard(): Promise<ClipboardOutcome> {
    const remote = this.remoteClipboard;
    if (!remote || remote.isEmpty()) return { status: "empty" };
    let plain: string | null = null;
    let imageError = "";
    const blobs: Record<string, Blob> = {};
    for (const item of remote.items()) {
      const mime = item.mimeType();
      const val = item.value();
      if (mime === "text/plain" && typeof val === "string") plain = val;
      if (mime === "text/plain" || mime === "text/html" || mime === "image/png") blobs[mime] = new Blob([val as BlobPart], { type: mime });
    }
    const hasImage = "image/png" in blobs;
    // The WebKit webview writes an image only from a click, and the desktop runtime writes text only, so desktop
    // WebKit builds hand images to the system clipboard through the app.
    if (hasImage && !WEB && isNativeWebKit()) {
      try {
        await localFileApi.writeClipboardImage(bytesToBase64(new Uint8Array(await blobs["image/png"]!.arrayBuffer())));
        return { status: "done", note: plain ? t("远程剪贴板同时有文本和图片，只复制了图片") : undefined };
      } catch (error) {
        imageError = describeError(error);
      }
    }
    // WebKit desktop builds write text through the native runtime, which never shows WebKit's paste confirmation.
    const viaBrowser = hasImage || !isNativeWebKit() || WEB;
    if (viaBrowser && typeof ClipboardItem === "function" && typeof navigator !== "undefined" && typeof navigator.clipboard?.write === "function") {
      try {
        await navigator.clipboard.write([new ClipboardItem(blobs)]);
        return { status: "done" };
      } catch {
        // Retried below with the text alone.
      }
    }
    if (plain !== null) {
      try {
        await copyText(plain);
        return { status: "done", note: hasImage ? imageNote(t("远程图片未能写入本机剪贴板，只复制了文本"), imageError) : undefined };
      } catch {
        return { status: "refused" };
      }
    }
    if (hasImage) return { status: "refused", note: imageNote(t("远程图片未能写入本机剪贴板，点击工具栏「取回远程剪贴板到本机」重试"), imageError) };
    return { status: Object.keys(blobs).length > 0 ? "refused" : "unsupported", note: Object.keys(blobs).length > 0 ? undefined : t("远程剪贴板的内容类型不受支持") };
  }

  async sendClipboard(data?: DataTransfer | null): Promise<ClipboardOutcome> {
    if (!this.options.clipboard || !this.ui || this.viewOnly) return { status: "refused" };
    try {
      if (data) return await this.sendDataTransfer(data);
      if (isNativeWebKit() && !WEB) {
        // navigator.clipboard.read() would pop up WebKit's paste confirmation; the native runtime reads text only.
        const text = await readText();
        if (!text) return { status: "empty" };
        return (await this.pasteToSession((clip) => clip.addText("text/plain", text))) ? { status: "done" } : { status: "refused" };
      }
      if (typeof navigator !== "undefined" && typeof navigator.clipboard?.read === "function") {
        const payload = { files: [] as File[], skipped: [] as string[], plain: "", html: "" };
        for (const item of await navigator.clipboard.read()) {
          for (const type of item.types) {
            const blob = await item.getType(type);
            if (type.startsWith("image/")) payload.files.push(new File([blob], "clipboard.png", { type }));
            else if (type === "text/plain") payload.plain = await blob.text();
            else if (type === "text/html") payload.html = await blob.text();
          }
        }
        return await this.sendPayload(payload);
      }
      const text = await readText();
      if (!text) return { status: "empty" };
      return (await this.pasteToSession((clip) => clip.addText("text/plain", text))) ? { status: "done" } : { status: "refused" };
    } catch {
      return { status: "refused" };
    }
  }

  setForeground(foreground: boolean): void {
    this.foreground = foreground;
  }

  async fetchClipboard(): Promise<ClipboardOutcome> {
    if (!this.options.clipboard || !this.ui) return { status: "refused" };
    try {
      const files = this.remoteFiles;
      const transfer = this.fileTransfer;
      if (files && transfer?.downloadFile) {
        return await saveRemoteFiles(files, (index) => transfer.downloadFile!(files[index]!, index).completion);
      }
      return await this.writeRemoteClipboard();
    } catch {
      return { status: "refused" };
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
