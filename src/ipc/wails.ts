import { clientId } from "./env";

const SERVICE_CALL = "main.Service.Call";
const CHANNEL_TOPIC_PREFIX = "channel://";

type WailsRuntime = typeof import("@wailsio/runtime");
type Unlisten = () => void;

interface WailsResponse {
  ok?: boolean;
  data?: unknown;
  error?: unknown;
}

let runtimePromise: Promise<WailsRuntime> | null = null;

function runtime(): Promise<WailsRuntime> {
  runtimePromise ??= import("@wailsio/runtime");
  return runtimePromise;
}

function request(cmd: string, args?: Record<string, unknown>) {
  if (!args) return { cmd, args: null };
  const { channel, clientId: stableClientId, ...body } = args;
  return {
    cmd,
    args: body,
    ...(channel === undefined ? {} : { channel }),
    ...(stableClientId === undefined ? {} : { clientId: stableClientId }),
  };
}

export async function callDesktop<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  await Promise.all([...channels.values()].map((channel) => channel.ready));
  const { Call } = await runtime();
  const response = (await Call.ByName(SERVICE_CALL, request(cmd, args))) as WailsResponse | null;
  if (!response || response.ok !== true) {
    throw response?.error ?? { code: "internal", message: "Wails 返回了无效响应" };
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
      if (!this.disposed) this.onmessage(message.data);
    });
  }
}

const channels = new Map<string, WailsChannel>();
let channelSequence = 0;

export function newWailsChannel(): WailsChannel {
  channelSequence += 1;
  const random = Math.random().toString(36).slice(2, 10);
  const id = `${clientId()}-c${channelSequence}-${random}`;
  const channel = new WailsChannel(id);
  channels.set(id, channel);
  void channel.ready.catch(() => disposeWailsChannel(channel));
  return channel;
}

export function disposeWailsChannel(channel: WailsChannel): void {
  channels.delete(channel.id);
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
