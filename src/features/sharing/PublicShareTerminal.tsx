import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

import { TERMINAL_FONT_STACKS, XTERM_DARK_THEME } from "../terminal/xtermTheme";
import { scheduleWebglRenderer } from "../terminal/xtermAddons";
import { createSelectionAutoCopy } from "../terminal/selectionAutoCopy";
import { isAutomaticTerminalReply } from "../terminal/terminalResponses";
import { createUserInputGate } from "../terminal/userInputGate";

export interface PublicShareTerminalHandle {
  write: (bytes: Uint8Array) => void;
  reset: () => void;
  focus: () => void;
}

export interface PublicShareTerminalProps {
  onInput: (data: string) => void;
  onSelectionCopy?: (text: string, error: unknown | null) => void;
  onHandle?: (handle: PublicShareTerminalHandle) => void;
  readOnly?: boolean;
}

export function PublicShareTerminal(props: PublicShareTerminalProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const onInputRef = useRef(props.onInput);
  const onSelectionCopyRef = useRef(props.onSelectionCopy);
  const onHandleRef = useRef(props.onHandle);
  const readOnlyRef = useRef(props.readOnly);
  onInputRef.current = props.onInput;
  onSelectionCopyRef.current = props.onSelectionCopy;
  onHandleRef.current = props.onHandle;
  readOnlyRef.current = props.readOnly;

  useEffect(() => {
    const host = hostRef.current;
    if (!host || termRef.current) return;
    const term = new Terminal({
      scrollback: 100_000,
      fontFamily: TERMINAL_FONT_STACKS.cascadia,
      fontSize: 14,
      cursorBlink: true,
      disableStdin: readOnlyRef.current ?? false,
      theme: XTERM_DARK_THEME,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    termRef.current = term;
    term.open(host);
    let disposed = false;
    const webgl = scheduleWebglRenderer(term, () => disposed);

    const fitLocal = () => {
      try {
        fit.fit();
      } catch {
      }
    };
    fitLocal();

    // A guest view never answers terminal queries: the host's terminal or
    // the server's headless screen does, so the shell gets one reply. Only
    // reply-shaped data raised by the guest's own input (a modified F3 looks
    // like a cursor report) goes through.
    const userInput = createUserInputGate(host);
    const dataDisposable = term.onData((data) => {
      if (!userInput.active() && isAutomaticTerminalReply(data)) return;
      onInputRef.current(data);
    });
    const autoCopy = createSelectionAutoCopy({
      isEnabled: () => true,
      getSelection: () => term.getSelection(),
      onCopied: (text) => onSelectionCopyRef.current?.(text, null),
      onError: (error) => onSelectionCopyRef.current?.("", error),
      delayMs: 0,
    });
    const selectionDisposable = term.onSelectionChange(() => autoCopy.notifySelectionChanged());

    const ro = new ResizeObserver(() => fitLocal());
    ro.observe(host);
    const onWindowResize = () => fitLocal();
    window.addEventListener("resize", onWindowResize);
    window.visualViewport?.addEventListener("resize", onWindowResize);

    onHandleRef.current?.({
      write: (bytes) => term.write(bytes),
      reset: () => term.reset(),
      focus: () => term.focus(),
    });

    return () => {
      ro.disconnect();
      window.removeEventListener("resize", onWindowResize);
      window.visualViewport?.removeEventListener("resize", onWindowResize);
      dataDisposable.dispose();
      userInput.dispose();
      selectionDisposable.dispose();
      autoCopy.dispose();
      disposed = true;
      webgl.cancel();
      term.dispose();
      termRef.current = null;
    };
  }, []);

  useEffect(() => {
    if (termRef.current) termRef.current.options.disableStdin = props.readOnly ?? false;
  }, [props.readOnly]);

  return <div ref={hostRef} className="h-full w-full min-h-0" />;
}
