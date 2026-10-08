import { askWails, messageWails, openWailsFile, saveWailsFile } from "../ipc/wails";
import { DEMO, TRANSPORT } from "../demo";
import { isMac } from "../app/platform";
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
};

type ChooseFn = (message: string, options: ChoiceOptions) => Promise<string | null>;

interface DialogHandlers {
  ask: AskFn;
  confirm: ConfirmFn;
  message: MessageFn;
  choose: ChooseFn;
}

let handlers: DialogHandlers | null = null;

type DeferredTask = () => void;

function createDeferredQueue() {
  let running = false;
  const pending: DeferredTask[] = [];
  return <T>(start: () => Promise<T>): Promise<T> =>
    new Promise<T>((resolve, reject) => {
      const run = () => {
        running = true;
        let result: Promise<T>;
        try {
          result = start();
        } catch (error) {
          result = Promise.reject(error);
        }
        result.then(resolve, reject).finally(() => {
          running = false;
          pending.shift()?.();
        });
      };
      if (running) pending.push(run);
      else run();
    });
}

const deferDialog = createDeferredQueue();
const deferPrompt = createDeferredQueue();

export function registerDialogHandlers(h: DialogHandlers) {
  handlers = h;
}

const NATIVE_BROWSER_DIALOG = DEMO || TRANSPORT === "web";

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
  return deferDialog(() => current.ask(message, options));
};

export const confirmDialog: ConfirmFn = (message) => {
  if (!handlers) return nativeConfirm(message);
  const current = handlers;
  return deferDialog(() => current.confirm(message));
};

export const messageBox: MessageFn = (message) => {
  if (!handlers) return nativeMessage(message);
  const current = handlers;
  return deferDialog(() => current.message(message));
};

export function askChoice(message: string, options: ChoiceOptions): Promise<string | null> {
  if (!handlers) return Promise.resolve(null);
  const current = handlers;
  return deferDialog(() => current.choose(message, options));
}

export async function pickLocalFile(): Promise<string | null> {
  if (DEMO) {
    return isMac()
      ? "~/Downloads/nginx-access.log"
      : "C:\\Users\\you\\Downloads\\nginx-access.log";
  }
  if (TRANSPORT === "web") {
    const picked = await pickBrowserFile();
    if (!picked) return null;
    const staged = await stageFile(picked);
    return staged.path;
  }
  return openWailsFile();
}

export async function pickKeyFile(): Promise<string | null> {
  if (DEMO) {
    return isMac() ? "~/.ssh/id_ed25519" : "C:\\Users\\you\\.ssh\\id_ed25519";
  }
  return openWailsFile([
    {
      name: "私钥文件",
      extensions: ["pem", "key", "ppk", "id_rsa", "id_ed25519", "openssh"],
    },
    { name: "所有文件", extensions: ["*"] },
  ]);
}

export async function pickSavePath(defaultName: string): Promise<string | null> {
  if (DEMO) {
    return isMac() ? `~/Desktop/${defaultName}` : `C:\\Users\\you\\Desktop\\${defaultName}`;
  }
  if (TRANSPORT === "web") {
    return requestSaveTarget(defaultName);
  }
  return saveWailsFile(defaultName);
}

export async function finishSave(target: string, name: string): Promise<string | null> {
  if (TRANSPORT !== "web") return target;
  const outcome = await deliverStaged(target, name);
  if (outcome === "cancelled") return null;
  if (outcome === "not-staged") {
    console.warn("[dialogs] 期望是暂存落点，但没有登记记录", target);
  }
  return name;
}

export function describeTarget(path: string, name: string): string {
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
  return deferPrompt(() => current(message, value, options));
}
