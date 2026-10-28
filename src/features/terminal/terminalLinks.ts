import type { ILinkHandler } from "@xterm/xterm";

// One link policy for URLs detected in the text (WebLinksAddon) and OSC 8
// hyperlinks (xterm's linkHandler). Links open with Ctrl+click (Cmd+click on
// macOS) unless the user chose plain click; the target, not the link text, is
// what gets shown and opened. While a program tracks the mouse (tmux, htop,
// vim with mouse=a) a click is the program's input, so a link additionally
// needs Shift, which xterm already treats as "bypass mouse tracking".

export type TerminalLinkActivation = "modifier" | "click";

// C0/C1 controls, zero-width and bidirectional formatting characters: never
// displayed, and a link containing them is not opened.
const UNSAFE_CHARS = /[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2060-\u2064\u2066-\u2069\ufeff]/g;
const DANGEROUS_SCHEMES = new Set(["javascript:", "data:", "vbscript:"]);
// OS protocol handlers that have been used to run code, change system state or
// leak credentials from a single click: refused rather than confirmed.
const DENIED_HANDLER_SCHEMES = new Set([
  "ms-msdt:",
  "search-ms:",
  "search:",
  "ms-search:",
  "ms-settings:",
  "ms-officecmd:",
  "ms-word:",
  "ms-excel:",
  "ms-powerpoint:",
  "ms-access:",
  "ms-appinstaller:",
  "ms-cxh:",
  "ms-cxh-full:",
  "ms-help:",
  "ms-its:",
  "mk:",
  "its:",
  "hcp:",
  "shell:",
  "smb:",
  "jar:",
]);
// mailto fields kept when opening; attach= and other client-specific fields
// are dropped.
const MAILTO_FIELDS = new Set(["subject", "body", "cc"]);
const DISPLAY_LIMIT = 1000;

export type TerminalLinkPlan =
  | { kind: "external"; url: string }
  | { kind: "file"; host: string; path: string }
  | { kind: "confirm"; url: string; scheme: string }
  | { kind: "blocked"; reason: string };

export function isMacPlatform(): boolean {
  if (typeof navigator === "undefined") return false;
  return /Mac|iPhone|iPad/i.test(navigator.platform ?? "") || /Macintosh/.test(navigator.userAgent);
}

export function linkModifierLabel(mac = isMacPlatform()): string {
  return mac ? "⌘" : "Ctrl";
}

export interface LinkModifierState {
  ctrlKey: boolean;
  metaKey: boolean;
  shiftKey?: boolean;
}

export function linkModifierHeld(event: LinkModifierState, mac = isMacPlatform()): boolean {
  return mac ? event.metaKey : event.ctrlKey;
}

export function shouldOpenLink(
  event: LinkModifierState,
  activation: TerminalLinkActivation,
  mac = isMacPlatform(),
  mouseTracking = false,
): boolean {
  if (mouseTracking && !event.shiftKey) return false;
  return activation === "click" || linkModifierHeld(event, mac);
}

export function linkOpenHint(activation: TerminalLinkActivation, mac = isMacPlatform(), mouseTracking = false): string {
  const keys = [...(activation === "modifier" ? [linkModifierLabel(mac)] : []), ...(mouseTracking ? ["Shift"] : [])];
  return keys.length ? `${keys.join("+")}+单击打开` : "单击打开";
}

function mailtoForOpening(url: URL): string {
  // RFC 6068 percent-encoding: "+" would not read as a space in a mail client.
  const query = [...url.searchParams]
    .filter(([key]) => MAILTO_FIELDS.has(key.toLowerCase()))
    .map(([key, value]) => `${encodeURIComponent(key)}=${encodeURIComponent(value)}`)
    .join("&");
  return `mailto:${url.pathname}${query ? `?${query}` : ""}`;
}

function safeDecode(value: string): string | null {
  try {
    return decodeURIComponent(value);
  } catch {
    return null;
  }
}

// cleanTerminalLink is the target without control or invisible characters,
// for copying.
export function cleanTerminalLink(uri: string): string {
  return uri.replace(UNSAFE_CHARS, "");
}

// displayTerminalLink renders a link target for people: control and
// invisible characters removed, IDN hosts in punycode (URL.href) so look-alike
// hosts stay distinguishable, file paths decoded, and long targets shortened.
export function displayTerminalLink(uri: string): string {
  const clean = uri.replace(UNSAFE_CHARS, "");
  let shown = clean;
  try {
    const url = new URL(clean);
    if (url.protocol === "file:") {
      const path = safeDecode(url.pathname)?.replace(UNSAFE_CHARS, "") ?? url.pathname;
      shown = `file://${url.host}${path}`;
    } else {
      shown = url.href;
    }
  } catch {
    shown = clean;
  }
  return shown.length > DISPLAY_LIMIT ? `${shown.slice(0, DISPLAY_LIMIT)}…` : shown;
}

export function planTerminalLink(uri: string): TerminalLinkPlan {
  if (new RegExp(UNSAFE_CHARS.source).test(uri)) return { kind: "blocked", reason: "链接包含控制字符" };
  let url: URL;
  try {
    url = new URL(uri);
  } catch {
    return { kind: "blocked", reason: "不是有效的链接" };
  }
  const scheme = url.protocol.toLowerCase();
  if (DANGEROUS_SCHEMES.has(scheme) || DENIED_HANDLER_SCHEMES.has(scheme)) {
    return { kind: "blocked", reason: `不允许打开 ${scheme} 链接` };
  }
  if (scheme === "http:" || scheme === "https:") {
    return url.hostname ? { kind: "external", url: url.href } : { kind: "blocked", reason: "链接缺少主机名" };
  }
  if (scheme === "mailto:") return { kind: "external", url: mailtoForOpening(url) };
  if (scheme === "file:") {
    const decoded = safeDecode(url.pathname);
    if (decoded === null || new RegExp(UNSAFE_CHARS.source).test(decoded)) {
      return { kind: "blocked", reason: "文件路径无效" };
    }
    // file:///C:/dir is a Windows drive path.
    const path = /^\/[A-Za-z]:(\/|$)/.test(decoded) ? decoded.slice(1) : decoded;
    return { kind: "file", host: url.hostname, path: path || "/" };
  }
  return { kind: "confirm", url: url.href, scheme: scheme.slice(0, -1) };
}

// fileLinkHostMatches compares a file:// link's host with the host the
// session's shell reported (OSC 7). An empty host or localhost names the
// machine the emitting program runs on, which is the session's host.
// Returns null when the session host is unknown.
export function fileLinkHostMatches(linkHost: string, sessionHost: string | null): boolean | null {
  const link = linkHost.toLowerCase();
  if (link === "" || link === "localhost") return true;
  if (!sessionHost) return null;
  const session = sessionHost.toLowerCase();
  if (link === session) return true;
  // `hostname` may report a short name on one side and an FQDN on the other.
  const isName = (host: string) => /^[a-z0-9-]+(\.[a-z0-9-]+)*$/.test(host) && !/^\d+(\.\d+)*$/.test(host);
  if (isName(link) && isName(session) && (!link.includes(".") || !session.includes("."))) {
    return link.split(".")[0] === session.split(".")[0];
  }
  return false;
}

export interface TerminalLinkEntry {
  name: string;
  path: string;
  kind: string;
}

export interface TerminalLinkDeps {
  sessionId: string;
  // Terminals inside a container see the container's filesystem, which the
  // session's file service cannot open.
  containerId?: string;
  // The session host's name, or null when unknown.
  sessionHost: () => string | null | Promise<string | null>;
  openExternal: (url: string) => void | Promise<void>;
  confirm: (message: string, title: string) => Promise<boolean>;
  notify: (kind: "info" | "error", text: string) => void;
  listDirectory: (sessionId: string, dir: string) => Promise<TerminalLinkEntry[]>;
  isEditableFile: (name: string) => boolean;
  openFile: (sessionId: string, path: string) => void;
  openDirectory: (sessionId: string, path: string) => void;
  describeError: (error: unknown) => string;
}

function parentDirectory(path: string): string | null {
  if (path === "/" || /^[A-Za-z]:\/?$/.test(path)) return null;
  const trimmed = path.replace(/\/+$/, "");
  const index = trimmed.lastIndexOf("/");
  if (index < 0) return null;
  if (index === 0) return "/";
  const parent = trimmed.slice(0, index);
  return /^[A-Za-z]:$/.test(parent) ? `${parent}/` : parent;
}

async function openFileLink(path: string, deps: TerminalLinkDeps): Promise<void> {
  const parent = parentDirectory(path);
  if (parent === null || path.endsWith("/")) {
    deps.openDirectory(deps.sessionId, path);
    return;
  }
  const trimmed = path.replace(/\/+$/, "");
  const name = trimmed.slice(trimmed.lastIndexOf("/") + 1);
  let entries: TerminalLinkEntry[];
  try {
    entries = await deps.listDirectory(deps.sessionId, parent);
  } catch (error) {
    deps.notify("error", `无法打开 ${path}：${deps.describeError(error)}`);
    return;
  }
  const entry = entries.find((candidate) => candidate.name === name);
  if (!entry) {
    deps.notify("error", `找不到 ${path}`);
    return;
  }
  if (entry.kind === "dir") {
    deps.openDirectory(deps.sessionId, entry.path || trimmed);
    return;
  }
  if (!deps.isEditableFile(name)) {
    deps.notify("info", `「${name}」不是文本文件，不能在线编辑；可以下载到本机后打开`);
    return;
  }
  deps.openFile(deps.sessionId, entry.path || trimmed);
}

export async function openTerminalLink(uri: string, deps: TerminalLinkDeps): Promise<void> {
  const shown = displayTerminalLink(uri);
  const plan = planTerminalLink(uri);
  switch (plan.kind) {
    case "blocked":
      deps.notify("error", `已阻止打开链接（${plan.reason}）：${shown}`);
      return;
    case "external":
      try {
        await deps.openExternal(plan.url);
      } catch (error) {
        deps.notify("error", `无法打开链接 ${shown}：${deps.describeError(error)}`);
      }
      return;
    case "confirm": {
      const ok = await deps.confirm(
        `终端中的链接使用 ${plan.scheme}: 协议，将交给系统中注册的程序处理。请仅在信任来源时打开。\n\n${shown}`,
        "打开外部链接",
      );
      if (!ok) return;
      try {
        await deps.openExternal(plan.url);
      } catch (error) {
        deps.notify("error", `无法打开链接 ${shown}：${deps.describeError(error)}`);
      }
      return;
    }
    case "file": {
      if (deps.containerId) {
        deps.notify("info", `容器终端中的文件链接无法在文件面板打开：${shown}`);
        return;
      }
      const matches = fileLinkHostMatches(plan.host, await deps.sessionHost());
      if (matches === false) {
        deps.notify("info", `链接指向其他主机 ${plan.host}，未打开：${shown}`);
        return;
      }
      if (matches === null) {
        const ok = await deps.confirm(
          `链接指向主机 ${plan.host}，无法确认它就是当前会话的主机。\n\n${shown}\n\n要在当前会话中打开 ${plan.path} 吗？`,
          "打开文件链接",
        );
        if (!ok) return;
      }
      await openFileLink(plan.path, deps);
      return;
    }
  }
}

export interface TerminalLinkController {
  // For the Terminal `linkHandler` option (OSC 8 hyperlinks).
  linkHandler: ILinkHandler;
  // For WebLinksAddon: the activation callback and its hover options.
  webLinkHandler: (event: MouseEvent, uri: string) => void;
  webLinkOptions: {
    hover: (event: MouseEvent, uri: string) => void;
    leave: (event: MouseEvent, uri: string) => void;
  };
  // The link target under the pointer, if any.
  hovered: () => string | null;
  // Forgets the hovered link and hides the tooltip.
  clear: () => void;
  dispose: () => void;
}

export interface TerminalLinkControllerOptions {
  // Element the tooltip is placed in (the xterm element or its host).
  tooltipHost: () => HTMLElement | null;
  // Element whose data-nx-link-armed attribute gates the pointer cursor.
  cursorHost: HTMLElement;
  activation: () => TerminalLinkActivation;
  // True while the running program tracks the mouse.
  mouseTracking?: () => boolean;
  open: (uri: string) => void;
  mac?: boolean;
}

export function createTerminalLinkController(options: TerminalLinkControllerOptions): TerminalLinkController {
  const mac = options.mac ?? isMacPlatform();
  let hoveredUri: string | null = null;
  let pointer = { x: 0, y: 0 };
  let keys: LinkModifierState = { ctrlKey: false, metaKey: false, shiftKey: false };
  let tooltip: HTMLDivElement | null = null;
  let listening = false;

  const tracking = () => options.mouseTracking?.() ?? false;
  const armed = () => shouldOpenLink(keys, options.activation(), mac, tracking());

  const hideTooltip = () => {
    tooltip?.remove();
    tooltip = null;
  };

  const render = () => {
    const isArmed = hoveredUri !== null && armed();
    options.cursorHost.dataset.nxLinkArmed = isArmed ? "true" : "false";
    if (!isArmed || hoveredUri === null) {
      hideTooltip();
      return;
    }
    const host = options.tooltipHost();
    if (!host) return;
    if (!tooltip) {
      tooltip = document.createElement("div");
      // xterm-hover keeps pointer events on the tooltip from re-targeting links.
      tooltip.className = "nx-term-link-tip xterm-hover";
      tooltip.setAttribute("role", "tooltip");
      const target = document.createElement("div");
      target.className = "nx-term-link-tip-target";
      const hint = document.createElement("div");
      hint.className = "nx-term-link-tip-hint";
      tooltip.append(target, hint);
      host.append(tooltip);
    }
    const [target, hint] = tooltip.children as unknown as [HTMLElement, HTMLElement];
    target.textContent = displayTerminalLink(hoveredUri);
    hint.textContent = linkOpenHint(options.activation(), mac, tracking());
    const rect = host.getBoundingClientRect();
    tooltip.style.left = `${Math.max(0, pointer.x - rect.left + 8)}px`;
    tooltip.style.top = `${Math.max(0, pointer.y - rect.top + 16)}px`;
  };

  const keysOf = (event: LinkModifierState): LinkModifierState => ({
    ctrlKey: event.ctrlKey,
    metaKey: event.metaKey,
    shiftKey: event.shiftKey === true,
  });

  const onKey = (event: KeyboardEvent) => {
    const next = keysOf(event);
    if (next.ctrlKey === keys.ctrlKey && next.metaKey === keys.metaKey && next.shiftKey === keys.shiftKey) return;
    keys = next;
    render();
  };

  const listen = (on: boolean) => {
    if (on === listening) return;
    listening = on;
    if (on) {
      window.addEventListener("keydown", onKey, true);
      window.addEventListener("keyup", onKey, true);
      window.addEventListener("blur", onBlur);
    } else {
      window.removeEventListener("keydown", onKey, true);
      window.removeEventListener("keyup", onKey, true);
      window.removeEventListener("blur", onBlur);
    }
  };

  const onBlur = () => {
    keys = { ctrlKey: false, metaKey: false, shiftKey: false };
    render();
  };

  const hover = (event: MouseEvent, uri: string) => {
    hoveredUri = uri;
    pointer = { x: event.clientX, y: event.clientY };
    keys = keysOf(event);
    listen(true);
    render();
  };

  const leave = () => {
    hoveredUri = null;
    listen(false);
    render();
  };

  const activate = (event: MouseEvent, uri: string) => {
    if (!shouldOpenLink(event, options.activation(), mac, tracking())) return;
    event.preventDefault();
    options.open(uri);
  };

  return {
    linkHandler: {
      activate,
      hover,
      leave,
      // Non-HTTP OSC 8 targets (file://, mailto:, ...) reach the policy
      // above, which decides what may open.
      allowNonHttpProtocols: true,
    },
    webLinkHandler: activate,
    webLinkOptions: { hover, leave },
    hovered: () => hoveredUri,
    clear: leave,
    dispose: () => {
      leave();
      delete options.cursorHost.dataset.nxLinkArmed;
    },
  };
}

// Touch long-press on a link: the target and where the menu should open.
export interface TerminalLinkMenuRequest {
  uri: string;
  x: number;
  y: number;
}

export const LINK_LONG_PRESS_MS = 500;
const LINK_LONG_PRESS_SLOP_PX = 10;

export interface LinkLongPressOptions {
  // Returns the link target at a viewport point, if any.
  probe: (x: number, y: number) => string | null;
  onLinkMenu: (request: TerminalLinkMenuRequest) => void;
}

// attachLinkLongPress offers the link under a held finger: a single touch that
// stays within a few pixels for LINK_LONG_PRESS_MS. Returns the detach function.
export function attachLinkLongPress(host: HTMLElement, options: LinkLongPressOptions): () => void {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let start: { x: number; y: number } | null = null;
  const cancel = () => {
    if (timer !== null) clearTimeout(timer);
    timer = null;
    start = null;
  };
  const onStart = (event: TouchEvent) => {
    cancel();
    if (event.touches.length !== 1) return;
    const touch = event.touches[0];
    const point = { x: touch.clientX, y: touch.clientY };
    start = point;
    timer = setTimeout(() => {
      timer = null;
      start = null;
      const uri = options.probe(point.x, point.y);
      if (uri) options.onLinkMenu({ uri, ...point });
    }, LINK_LONG_PRESS_MS);
  };
  const onMove = (event: TouchEvent) => {
    const touch = event.touches[0];
    if (!start || !touch) return;
    if (Math.hypot(touch.clientX - start.x, touch.clientY - start.y) > LINK_LONG_PRESS_SLOP_PX) cancel();
  };
  host.addEventListener("touchstart", onStart, { capture: true, passive: true });
  host.addEventListener("touchmove", onMove, { capture: true, passive: true });
  host.addEventListener("touchend", cancel, true);
  host.addEventListener("touchcancel", cancel, true);
  return () => {
    cancel();
    host.removeEventListener("touchstart", onStart, true);
    host.removeEventListener("touchmove", onMove, true);
    host.removeEventListener("touchend", cancel, true);
    host.removeEventListener("touchcancel", cancel, true);
  };
}

// probeTerminalLink asks xterm for the link at a viewport point by sending its
// link detection a mousemove there, then forgets the hover so no tooltip stays
// behind. The event is dispatched on the screen element without bubbling: the
// link detection listens there, while xterm's mouse reporting listens on the
// .xterm ancestor, so a program tracking any motion (mode 1003) gets no report.
export function probeTerminalLink(
  screen: Element | null | undefined,
  controller: Pick<TerminalLinkController, "hovered" | "clear">,
  x: number,
  y: number,
): string | null {
  if (!screen) return null;
  screen.dispatchEvent(new MouseEvent("mousemove", { clientX: x, clientY: y, bubbles: false }));
  const uri = controller.hovered();
  controller.clear();
  return uri;
}
