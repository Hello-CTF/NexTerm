import type { TrzszFilter, TrzszOptions } from "trzsz";
import { nextTrzszTail, trzszRequestIn } from "./trzszDetect";
import type { TrzszRequestContext } from "./trzszFiles";
import type { StreamMode } from "../streamClasses";
import { t } from "../../../i18n";

// One trzsz (trz / tsz) endpoint per terminal view. Only live output starts
// a transfer: replayed history never does. Until a request
// is seen trzsz.js is not loaded and output goes straight to xterm; while a
// transfer runs, output belongs to trzsz.js and keyboard input is held back
// (Ctrl+C cancels), so it reaches neither the host nor broadcast targets.

export type TrzszFilterLike = Pick<
  TrzszFilter,
  "processServerOutput" | "processTerminalInput" | "setTerminalColumns" | "isTransferringFiles" | "stopTransferringFiles"
>;
export type TrzszFilterFactory = (options: TrzszOptions) => TrzszFilterLike;

export interface TrzszSessionOptions {
  enabled: () => boolean;
  // With several devices on one terminal, only the one that typed last answers.
  inputOwner: () => boolean;
  windowsShell: () => boolean;
  columns: () => number;
  host: () => string | null;
  sendToServer: (data: Uint8Array) => void;
  writeLocal: (text: string) => void;
  onActiveChange?: (active: boolean) => void;
  onError?: (error: unknown) => void;
  loadFactory?: () => Promise<TrzszFilterFactory>;
  pollMs?: number;
  // How long a recognized request may take to turn into a transfer.
  startTimeoutMs?: number;
}

export interface TrzszSession {
  // Offers an output chunk with its stream mode; true when trzsz.js consumed
  // it and it must not be written to xterm. Only live output starts a
  // transfer. A running transfer also takes answered output (bytes produced
  // while this view was briefly detached), and replayed history ends it.
  handleOutput: (bytes: Uint8Array, mode: StreamMode) => boolean;
  // Offers user input; true when a transfer holds it back.
  handleInput: (data: string) => boolean;
  active: () => boolean;
  cancel: () => void;
  dispose: () => void;
}

const EMPTY = new Uint8Array(0);
const encoder = new TextEncoder();
const decoder = new TextDecoder();

const sessions = new Set<{ transferring: () => boolean; host: () => string | null }>();

// The host of the terminal a picker request belongs to. trzsz.js does not say
// which terminal asks, so a name is given only while exactly one is transferring.
export function currentTrzszRequestContext(): TrzszRequestContext {
  const busy = [...sessions].filter((session) => session.transferring());
  return { host: busy.length === 1 ? busy[0].host() : null };
}

async function loadDefaultFactory(): Promise<TrzszFilterFactory> {
  const [{ TrzszFilter: Filter }, { installDefaultTrzszPickers }] = await Promise.all([
    import("trzsz"),
    import("./trzszPickers"),
  ]);
  installDefaultTrzszPickers();
  return (options) => new Filter(options);
}

export function createTrzszSession(options: TrzszSessionOptions): TrzszSession {
  const pollMs = options.pollMs ?? 200;
  const startTimeoutMs = options.startTimeoutMs ?? 3000;
  let filter: TrzszFilterLike | null = null;
  let loading: Promise<void> | null = null;
  let pending: Uint8Array[] = [];
  let echo: unknown = null;
  let tail: Uint8Array = EMPTY;
  let activeState = false;
  let poll: ReturnType<typeof setInterval> | null = null;
  let armedAt = 0;
  let disposed = false;
  let columns = 0;

  // The progress bar follows the terminal width; it is checked as transfer
  // data arrives instead of subscribing to every resize.
  const syncColumns = () => {
    const now = options.columns();
    if (now !== columns) {
      columns = now;
      filter?.setTerminalColumns(now);
    }
  };

  const transferring = () => filter?.isTransferringFiles() === true;
  const registration = { transferring, host: options.host };
  sessions.add(registration);

  const setActive = (next: boolean) => {
    if (activeState === next) return;
    activeState = next;
    options.onActiveChange?.(next);
  };

  const stopPoll = () => {
    if (poll !== null) clearInterval(poll);
    poll = null;
  };

  // trzsz.js reports no start or end of a transfer: it is polled from the
  // moment a request is seen until the transfer it started has finished.
  const check = () => {
    const now = transferring();
    const was = activeState;
    setActive(now);
    if (!now && (was || Date.now() - armedAt > startTimeoutMs)) stopPoll();
  };

  const arm = () => {
    armedAt = Date.now();
    if (poll === null) poll = setInterval(check, pollMs);
  };

  const writeToTerminal = (output: string | ArrayBuffer | Uint8Array | Blob) => {
    // The request chunk itself was already written through the normal path.
    if (output === echo || disposed) return;
    if (typeof output === "string") options.writeLocal(output);
    else if (output instanceof Uint8Array) options.writeLocal(decoder.decode(output));
    else if (output instanceof ArrayBuffer) options.writeLocal(decoder.decode(new Uint8Array(output)));
  };

  const feed = (chunk: Uint8Array) => {
    if (!filter) return;
    echo = chunk;
    try {
      filter.processServerOutput(chunk);
    } finally {
      echo = null;
    }
    arm();
  };

  const ensureLoaded = () => {
    if (loading) return;
    loading = (options.loadFactory ?? loadDefaultFactory)()
      .then((factory) => {
        if (disposed) return;
        filter = factory({
          writeToTerminal,
          sendToServer: (input) => {
            if (disposed) return;
            options.sendToServer(typeof input === "string" ? encoder.encode(input) : input);
          },
          terminalColumns: (columns = options.columns()),
          isWindowsShell: options.windowsShell(),
        });
        const queued = pending;
        pending = [];
        for (const chunk of queued) feed(chunk);
      })
      .catch((error: unknown) => {
        loading = null;
        pending = [];
        options.onError?.(error);
      });
  };

  return {
    handleOutput: (bytes, mode) => {
      if (filter && transferring()) {
        if (mode === "replay") {
          filter.stopTransferringFiles();
          return false;
        }
        setActive(true);
        syncColumns();
        filter.processServerOutput(bytes.slice());
        return true;
      }
      if (mode !== "live" || !options.enabled()) {
        tail = EMPTY;
        return false;
      }
      const request = trzszRequestIn(tail, bytes);
      tail = nextTrzszTail(tail, bytes);
      if (!request) return false;
      if (!options.inputOwner()) {
        options.writeLocal(t("\r\n\u001b[33m[trzsz] 文件传输由最近在此终端输入的设备处理\u001b[0m\r\n"));
        return false;
      }
      tail = EMPTY;
      if (filter) feed(request);
      else {
        pending.push(request);
        ensureLoaded();
      }
      return false;
    },
    handleInput: (data) => {
      if (!filter || !transferring()) return false;
      setActive(true);
      filter.processTerminalInput(data);
      return true;
    },
    active: transferring,
    cancel: () => {
      if (transferring()) filter?.stopTransferringFiles();
    },
    dispose: () => {
      if (disposed) return;
      if (transferring()) filter?.stopTransferringFiles();
      disposed = true;
      stopPoll();
      sessions.delete(registration);
      pending = [];
      setActive(false);
    },
  };
}
