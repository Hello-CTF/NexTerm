import { askWails, messageWails, openWailsFile, openWailsFolder, saveWailsFile } from "../ipc/wails";
import { TRANSPORT, WEB } from "../ipc/env";
import {
  deliverStaged,
  dropStaged,
  isStagedPath,
  pickBrowserFile,
  requestSaveTarget,
  stageFile,
} from "../ipc/webFiles";

export { dropStaged as discardStaged };

type AskOptions = { title?: string; kind?: "info" | "warning" | "error" };

type AskFn = (message: string, options?: AskOptions) => Promise<boolean>;
type ConfirmFn = (message: string) => Promise<boolean>;
type MessageFn = (message: string) => Promise<void>;

export interface ChoiceOption {
  key: string;
  label: string;
  hint?: string;
  danger?: boolean;
  primary?: boolean;
}

export type ChoiceOptions = {
  title?: string;
  level?: "info" | "warning";
  choices: ChoiceOption[];
  rememberLabel?: string;
};

export interface ChoiceResult {
  key: string;
  remember: boolean;
}

type ChooseFn = (message: string, options: ChoiceOptions) => Promise<ChoiceResult | null>;

interface DialogHandlers {
  ask: AskFn;
  confirm: ConfirmFn;
  message: MessageFn;
  choose: ChooseFn;
}

let handlers: DialogHandlers | null = null;

// The account epoch is injected instead of imported from the app store: a static store import
// here would make this module part of the store's import cycle and break module mocking for
// dynamic importers.
let accountEpochSource: () => number = () => 0;

export function registerDialogEpochSource(source: () => number): void {
  accountEpochSource = source;
}

type DeferredTask = {
  start: () => void;
  cancel: () => void;
  epoch: number;
};

// Dialog queues are account-generation aware: a queued task only starts while its account
// epoch is current. When the active request settles (including via cancelAccountDialogs on an
// account switch), every stale queued task is settled with its cancellation result and dropped,
// so no queued dialog can open or mutate across the boundary.
function createDeferredQueue() {
  let running = false;
  const pending: DeferredTask[] = [];
  const advance = () => {
    running = false;
    for (;;) {
      const next = pending.shift();
      if (!next) return;
      if (next.epoch === accountEpochSource()) {
        next.start();
        return;
      }
      next.cancel();
    }
  };
  return <T>(start: () => Promise<T>, cancel: () => T): Promise<T> =>
    new Promise<T>((resolve, reject) => {
      const task: DeferredTask = {
        start: () => {
          running = true;
          let result: Promise<T>;
          try {
            result = start();
          } catch (error) {
            result = Promise.reject(error);
          }
          result.then(resolve, reject).finally(advance);
        },
        cancel: () => resolve(cancel()),
        epoch: accountEpochSource(),
      };
      if (running) pending.push(task);
      else task.start();
    });
}

const deferDialog = createDeferredQueue();
const deferPrompt = createDeferredQueue();

export function registerDialogHandlers(h: DialogHandlers) {
  handlers = h;
}

const NATIVE_BROWSER_DIALOG = WEB;

const nativeAsk: AskFn = NATIVE_BROWSER_DIALOG
  ? async (message) => window.confirm(message)
  : askWails;
const nativeConfirm: ConfirmFn = NATIVE_BROWSER_DIALOG
  ? async (message) => window.confirm(message)
  : askWails;
const nativeMessage: MessageFn = NATIVE_BROWSER_DIALOG
  ? async (message) => {
      window.alert(message);
    }
  : messageWails;

export const ask: AskFn = (message, options) => {
  if (!handlers) return nativeAsk(message, options);
  const current = handlers;
  return deferDialog(() => current.ask(message, options), () => false);
};

export const confirmDialog: ConfirmFn = (message) => {
  if (!handlers) return nativeConfirm(message);
  const current = handlers;
  return deferDialog(() => current.confirm(message), () => false);
};

export const messageBox: MessageFn = (message) => {
  if (!handlers) return nativeMessage(message);
  const current = handlers;
  return deferDialog(() => current.message(message), () => undefined);
};

export function askChoiceRemember(message: string, options: ChoiceOptions): Promise<ChoiceResult | null> {
  if (!handlers) return Promise.resolve(null);
  const current = handlers;
  return deferDialog(() => current.choose(message, options), () => null);
}

export function askChoice(message: string, options: ChoiceOptions): Promise<string | null> {
  return askChoiceRemember(message, options).then((result) => (result ? result.key : null));
}

export async function pickLocalFile(): Promise<string | null> {
  if (TRANSPORT === "web") {
    const picked = await pickBrowserFile();
    if (!picked) return null;
    const staged = await stageFile(picked);
    return staged.path;
  }
  return openWailsFile();
}

export async function pickLocalFolder(): Promise<string | null> {
  return openWailsFolder();
}

export async function pickKeyFile(): Promise<string | null> {
  return openWailsFile([
    {
      name: "私钥文件",
      extensions: ["pem", "key", "ppk", "id_rsa", "id_ed25519", "openssh"],
    },
    { name: "所有文件", extensions: ["*"] },
  ]);
}

export async function pickSavePath(defaultName: string): Promise<string | null> {
  if (TRANSPORT === "web") {
    return requestSaveTarget(defaultName);
  }
  return saveWailsFile(defaultName);
}

// The Wails mobile shells have no save dialog; the backend maps this marker to the app download directory.
const MOBILE_DOWNLOAD_PREFIX = "nexterm-mobile-download:";

function isNativeMobileShell(): boolean {
  if (TRANSPORT === "web" || typeof navigator === "undefined") return false;
  const ua = navigator.userAgent;
  return /Android|iPhone|iPad|iPod/i.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1);
}

// pickDownloadTarget is pickSavePath for remote file downloads, whose local path the backend resolves itself.
export async function pickDownloadTarget(defaultName: string): Promise<string | null> {
  if (isNativeMobileShell()) return `${MOBILE_DOWNLOAD_PREFIX}${defaultName}`;
  return pickSavePath(defaultName);
}

export async function finishSave(target: string, name: string): Promise<string | null> {
  if (target.startsWith(MOBILE_DOWNLOAD_PREFIX)) return `应用下载目录/${name}`;
  if (TRANSPORT !== "web") return target;
  const outcome = await deliverStaged(target, name);
  if (outcome === "cancelled") return null;
  if (outcome === "not-staged") {
    console.warn("[dialogs] Expected a staged save target, but no registration was found", target);
  }
  return name;
}

export function describeTarget(path: string, name: string): string {
  if (path.startsWith(MOBILE_DOWNLOAD_PREFIX)) return `应用下载目录/${name}`;
  return TRANSPORT === "web" && isStagedPath(path) ? name : path;
}

export type PromptOptions = {
  multiLine?: boolean;
  secret?: boolean;
};

type PromptFn = (
  message: string,
  value: string,
  options: PromptOptions,
) => Promise<string | null>;

let promptHandler: PromptFn | null = null;

export function registerPromptHandler(fn: PromptFn) {
  promptHandler = fn;
}

export function promptText(
  message: string,
  value = "",
  options: PromptOptions = {},
): Promise<string | null> {
  if (!promptHandler) {
    return Promise.resolve(NATIVE_BROWSER_DIALOG ? window.prompt(message, value) : null);
  }
  const current = promptHandler;
  return deferPrompt(() => current(message, value, options), () => null);
}
