// RemoteKeyboard sits in front of the remote client's own key handling. It sends Command as Ctrl when asked, and
// turns a paste chord into "send the local clipboard, then paste", so the remote side pastes what was copied here.

export interface RemoteKeyboardTarget {
  sendKey(code: string, down: boolean): void;
  sendClipboard(data?: DataTransfer | null): Promise<boolean>;
}

export interface RemoteKeyboardOptions {
  // Command keys go out as the matching Ctrl key. The caller enables it only on a Mac.
  commandAsCtrl: boolean;
  // Ctrl+V or Command+V first sends the local clipboard.
  syncPaste: boolean;
  // Time the remote side gets to take in a new clipboard before the paste keys arrive.
  pasteDelayMs: number;
}

const COMMAND_TO_CTRL: Record<string, string> = { MetaLeft: "ControlLeft", MetaRight: "ControlRight" };

function wait(ms: number): Promise<void> {
  return ms > 0 ? new Promise((resolve) => setTimeout(resolve, ms)) : Promise.resolve();
}

export class RemoteKeyboard {
  private readonly ctrlDown = new Set<string>();
  private readonly commandDown = new Set<string>();
  // Keys pressed while Command is down. macOS sends no keyup for them, so they are released with Command.
  private readonly pressedWithCommand = new Set<string>();
  private pasting = false;
  private pendingPaste: { modifier: string; viaCtrl: boolean; delayMs: number } | null = null;
  private pendingPasteTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    private readonly target: () => RemoteKeyboardTarget | null,
    private readonly options: () => RemoteKeyboardOptions,
    // Called on a paste keydown before the browser runs its default action. Returning true means the caller focused a
    // hidden paste target and left preventDefault() off so a native ClipboardEvent("paste") will deliver clipboardData
    // without triggering WebKit's programmatic paste confirmation menu; if no paste event arrives by the next macrotask
    // (e.g. in synthetic keydown tests), the fallback still calls sendClipboard().
    private readonly prepareNativePaste?: (event: KeyboardEvent) => boolean,
  ) {}

  // handle returns true when it consumed the event; the caller then keeps it from the remote client.
  handle(event: KeyboardEvent): boolean {
    if (event.type !== "keydown" && event.type !== "keyup") return false;
    const down = event.type === "keydown";
    const { code } = event;
    const options = this.options();
    if (code === "ControlLeft" || code === "ControlRight") {
      if (down) this.ctrlDown.add(code);
      else this.ctrlDown.delete(code);
      return false;
    }
    const ctrl = COMMAND_TO_CTRL[code];
    if (ctrl) {
      if (down) this.commandDown.add(code);
      else this.commandDown.delete(code);
      if (!down && this.commandDown.size === 0) this.releasePressedWithCommand();
      if (!options.commandAsCtrl) return false;
      if (!down || !event.repeat) this.target()?.sendKey(ctrl, down);
      return true;
    }
    if (code === "KeyV" && options.syncPaste && !event.altKey && !event.shiftKey && (event.ctrlKey || event.metaKey)) {
      if (down && !event.repeat && !this.pasting && this.pendingPasteTimer === null) {
        const viaCtrl = event.ctrlKey || options.commandAsCtrl;
        const modifier = viaCtrl ? "ControlLeft" : "MetaLeft";
        if (this.prepareNativePaste?.(event)) {
          this.pendingPaste = { modifier, viaCtrl, delayMs: options.pasteDelayMs };
          this.pendingPasteTimer = setTimeout(() => {
            const pending = this.pendingPaste;
            this.pendingPaste = null;
            this.pendingPasteTimer = null;
            if (pending && !this.pasting) {
              void this.paste(pending.modifier, pending.viaCtrl, pending.delayMs);
            }
          }, 0);
        } else {
          void this.paste(modifier, viaCtrl, options.pasteDelayMs);
        }
      }
      return true;
    }
    if (this.commandDown.size > 0) {
      if (down) this.pressedWithCommand.add(code);
      else this.pressedWithCommand.delete(code);
    }
    return false;
  }

  // handlePaste consumes a native DOM paste/drop DataTransfer, sending its text/image/file content directly to the
  // remote clipboard and then pressing the paste shortcut on the remote side.
  handlePaste(data: DataTransfer | null): boolean {
    const options = this.options();
    if (!options.syncPaste) return false;
    if (this.pendingPasteTimer !== null) {
      clearTimeout(this.pendingPasteTimer);
      this.pendingPasteTimer = null;
    }
    const pending = this.pendingPaste;
    this.pendingPaste = null;
    if (this.pasting) return true;
    const viaCtrl = pending ? pending.viaCtrl : this.ctrlDown.size > 0 || options.commandAsCtrl || this.commandDown.size === 0;
    const modifier = pending ? pending.modifier : viaCtrl ? "ControlLeft" : "MetaLeft";
    const delayMs = pending ? pending.delayMs : options.pasteDelayMs;
    void this.paste(modifier, viaCtrl, delayMs, data);
    return true;
  }

  // reset forgets every held key, for focus loss and reconnects; the client releases its own keys.
  reset(): void {
    if (this.pendingPasteTimer !== null) {
      clearTimeout(this.pendingPasteTimer);
      this.pendingPasteTimer = null;
    }
    this.pendingPaste = null;
    this.ctrlDown.clear();
    this.commandDown.clear();
    this.pressedWithCommand.clear();
  }

  private releasePressedWithCommand(): void {
    const target = this.target();
    for (const code of this.pressedWithCommand) target?.sendKey(code, false);
    this.pressedWithCommand.clear();
  }

  private modifierHeld(viaCtrl: boolean): boolean {
    if (!viaCtrl) return this.commandDown.size > 0;
    return this.ctrlDown.size > 0 || (this.options().commandAsCtrl && this.commandDown.size > 0);
  }

  private async paste(modifier: string, viaCtrl: boolean, delayMs: number, data?: DataTransfer | null): Promise<void> {
    this.pasting = true;
    try {
      const sent = (await this.target()?.sendClipboard(data).catch(() => false)) ?? false;
      if (sent) await wait(delayMs);
      const target = this.target();
      if (!target) return;
      // The modifier may have been let go while the clipboard was read; the paste still carries it.
      const held = this.modifierHeld(viaCtrl);
      if (!held) target.sendKey(modifier, true);
      target.sendKey("KeyV", true);
      target.sendKey("KeyV", false);
      if (!held) target.sendKey(modifier, false);
    } finally {
      this.pasting = false;
    }
  }
}
