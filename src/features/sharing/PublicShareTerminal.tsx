import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebglAddon } from "@xterm/addon-webgl";
import "@xterm/xterm/css/xterm.css";

import { XTERM_DARK_THEME } from "../terminal/xtermTheme";
import { createSelectionAutoCopy } from "../terminal/selectionAutoCopy";

export interface PublicShareTerminalHandle {
  write: (bytes: Uint8Array) => void;
  focus: () => void;
}

export interface PublicShareTerminalProps {
  onInput: (data: string) => void;
  onSelectionCopy?: (text: string, error: unknown | null) => void;
  onHandle?: (handle: PublicShareTerminalHandle) => void;
}

export function PublicShareTerminal(props: PublicShareTerminalProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const onInputRef = useRef(props.onInput);
  const onSelectionCopyRef = useRef(props.onSelectionCopy);
  const onHandleRef = useRef(props.onHandle);
  onInputRef.current = props.onInput;
  onSelectionCopyRef.current = props.onSelectionCopy;
  onHandleRef.current = props.onHandle;

  useEffect(() => {
    const host = hostRef.current;
    if (!host || termRef.current) return;
    const term = new Terminal({
      scrollback: 100_000,
      fontFamily: "'Cascadia Mono', 'Cascadia Code', Consolas, 'Courier New', monospace",
      fontSize: 14,
      cursorBlink: true,
      theme: XTERM_DARK_THEME,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    termRef.current = term;
    term.open(host);
    try {
      term.loadAddon(new WebglAddon());
    } catch {
    }

    const fitLocal = () => {
      try {
        fit.fit();
      } catch {
      }
    };
    fitLocal();

    const dataDisposable = term.onData((data) => onInputRef.current(data));
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
      focus: () => term.focus(),
    });

    return () => {
      ro.disconnect();
      window.removeEventListener("resize", onWindowResize);
      window.visualViewport?.removeEventListener("resize", onWindowResize);
      dataDisposable.dispose();
      selectionDisposable.dispose();
      autoCopy.dispose();
      term.dispose();
      termRef.current = null;
    };
  }, []);

  return <div ref={hostRef} className="h-full w-full min-h-0" />;
}
