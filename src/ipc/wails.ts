import { clientId } from "./env";

const CHANNEL_TOPIC_PREFIX = "channel://";
const DESKTOP_BINARY_TYPE = "nexterm.desktop-binary";

// Uint8Array.fromBase64 (TC39) is not in every lib.dom yet; feature-detect it at call time so tests can toggle it.
type Base64Decoder = { fromBase64?: (text: string) => Uint8Array };

function decodeBase64(text: string): Uint8Array {
  const native = (Uint8Array as unknown as Base64Decoder).fromBase64;
  if (typeof native === "function") return native.call(Uint8Array, text);
  // Fallback measured fastest in V8 (about 1 ms/MB warm); Uint8Array.from(atob(...), map) is ~40x slower.
  const decoded = atob(text);
  const bytes = new Uint8Array(decoded.length);
  for (let index = 0; index < decoded.length; index += 1) {
    bytes[index] = decoded.charCodeAt(index);
  }
  return bytes;
}

function decodeChannelData(raw: unknown): unknown {
  if (typeof raw !== "object" || raw === null) return raw;
  const payload = raw as { type?: unknown; data?: unknown };
  if (payload.type !== DESKTOP_BINARY_TYPE || typeof payload.data !== "string") return raw;
  return decodeBase64(payload.data);
}

type WailsRuntime = typeof import("@wailsio/runtime");
type WailsService = typeof import("./bindings/github.com/Hello-CTF/NexTerm/cmd/nexterm-desktop/service");
type Unlisten = () => void;

interface WailsResponse {
  ok?: boolean;
  data?: unknown;
  error?: unknown;
}

let runtimePromise: Promise<WailsRuntime> | null = null;
let bindingsPromise: Promise<WailsService> | null = null;

function runtime(): Promise<WailsRuntime> {
  runtimePromise ??= import("@wailsio/runtime");
  return runtimePromise;
}

function bindings(): Promise<WailsService> {
  bindingsPromise ??= import("./bindings/github.com/Hello-CTF/NexTerm/cmd/nexterm-desktop/service");
  return bindingsPromise;
}

function request(cmd: string, args?: Record<string, unknown>) {
  if (!args) return { cmd, args: null };
  const { channel, clientId: stableClientId, ...body } = args;
  return {
    cmd,
    args: body,
    ...(channel === undefined ? {} : { channel }),
    ...(typeof stableClientId === "string" ? { clientId: stableClientId } : {}),
  };
}

export async function callDesktop<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  // The gate exists so a channel is subscribed before the backend can push on it; only the channel this call
  // hands over needs it, so unrelated calls no longer wait on other channels' subscription.
  const target = args?.channel;
  if (target instanceof WailsChannel) await target.ready;
  const { Call } = await bindings();
  const response = (await Call(request(cmd, args))) as WailsResponse | null;
  if (!response || response.ok !== true) {
    throw response?.error ?? { code: "internal", message: "应用返回了无效响应" };
  }
  return response.data as T;
}

export async function listenWailsEvent<T>(
  event: string,
  handler: (payload: T) => void,
): Promise<Unlisten> {
  const { Events } = await runtime();
  return Events.On(event, (message) => handler(message.data as T));
}

export async function consumeDeepLinks(): Promise<string[]> {
  const { ConsumeDeepLinks } = await bindings();
  const links = await ConsumeDeepLinks();
  return links ?? [];
}

export async function openExternalURL(url: string): Promise<void> {
  const { Browser } = await runtime();
  await Browser.OpenURL(url);
}

export class WailsChannel {
  readonly id: string;
  onmessage: (message: unknown) => void = () => {};
  readonly ready: Promise<void>;
  private disposed = false;
  private off: Unlisten | null = null;

  constructor(id: string) {
    this.id = id;
    this.ready = this.listen();
  }

  toJSON(): string {
    return this.id;
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.off?.();
    this.off = null;
  }

  private async listen(): Promise<void> {
    const { Events } = await runtime();
    if (this.disposed) return;
    this.off = Events.On(`${CHANNEL_TOPIC_PREFIX}${this.id}`, (message) => {
      if (!this.disposed) this.onmessage(decodeChannelData(message.data));
    });
  }
}

let channelSequence = 0;

export function newWailsChannel(): WailsChannel {
  channelSequence += 1;
  const random = Math.random().toString(36).slice(2, 10);
  const id = `${clientId()}-c${channelSequence}-${random}`;
  const channel = new WailsChannel(id);
  void channel.ready.catch(() => disposeWailsChannel(channel));
  return channel;
}

export function disposeWailsChannel(channel: WailsChannel): void {
  channel.dispose();
}

export async function askWails(
  message: string,
  options: { title?: string; kind?: "info" | "warning" | "error" } = {},
): Promise<boolean> {
  const { Dialogs } = await runtime();
  const dialog =
    options.kind === "warning"
      ? Dialogs.Warning
      : options.kind === "error"
        ? Dialogs.Error
        : options.kind === "info"
          ? Dialogs.Info
          : Dialogs.Question;
  const selected = await dialog({
    Title: options.title,
    Message: message,
    Buttons: [
      { Label: "取消", IsCancel: true },
      { Label: "确定", IsDefault: true },
    ],
  });
  return selected === "确定";
}

export async function messageWails(message: string): Promise<void> {
  const { Dialogs } = await runtime();
  await Dialogs.Info({ Message: message, Buttons: [{ Label: "确定", IsDefault: true }] });
}

export async function openWailsFile(
  filters: { name: string; extensions: string[] }[] = [],
): Promise<string | null> {
  const { Dialogs } = await runtime();
  const nativeFilters = filters.map((filter) => ({
    DisplayName: filter.name,
    Pattern: filter.extensions
      .map((extension) => (extension === "*" ? "*" : `*.${extension}`))
      .join(";"),
  }));
  const selected = await Dialogs.OpenFile({
    AllowsMultipleSelection: false,
    CanChooseFiles: true,
    CanChooseDirectories: false,
    ...(nativeFilters.length > 0 ? { Filters: nativeFilters } : {}),
  });
  return selected || null;
}

export async function openWailsFolder(): Promise<string | null> {
  const { Dialogs } = await runtime();
  const selected = await Dialogs.OpenFile({
    AllowsMultipleSelection: false,
    CanChooseFiles: false,
    CanChooseDirectories: true,
  });
  return typeof selected === "string" && selected ? selected : null;
}

export async function saveWailsFile(defaultName: string): Promise<string | null> {
  const { Dialogs } = await runtime();
  const selected = await Dialogs.SaveFile({ Filename: defaultName });
  return selected || null;
}

export async function minimiseWindow(): Promise<void> {
  const { Window } = await runtime();
  await Window.Minimise();
}

export async function toggleMaximiseWindow(): Promise<void> {
  const { Window } = await runtime();
  await Window.ToggleMaximise();
}

export async function closeWindow(): Promise<void> {
  const { Window } = await runtime();
  await Window.Close();
}
