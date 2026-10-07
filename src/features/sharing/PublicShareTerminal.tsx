import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebglAddon } from "@xterm/addon-webgl";
import "@xterm/xterm/css/xterm.css";

export interface PublicShareTerminalHandle {
  write: (bytes: Uint8Array) => void;
  focus: () => void;
}

export interface PublicShareTerminalProps {
  onInput: (data: string) => void;
  onSelectionCopy?: (text: string, error: unknown | null) => void;
  onHandle?: (handle: PublicShareTerminalHandle) => void;
}

const PUBLIC_TERM_THEME = {
  background: "#101217",
  foreground: "#c6cbd6",
  cursorAccent: "#101217",
  selectionBackground: "#2c3e5d",
  scrollbarSliderBackground: "#31363f",
  scrollbarSliderHoverBackground: "#3e444f",
  scrollbarSliderActiveBackground: "#4d5563",
  black: "#101217",
  brightBlack: "#5c6472",
  red: "#e87b7b",
  brightRed: "#f0a0a0",
  green: "#7fd6a4",
  brightGreen: "#a3e6bd",
  yellow: "#e9c489",
  brightYellow: "#f0d6a4",
  blue: "#79a8f8",
  brightBlue: "#9dc0fb",
  magenta: "#b79ce8",
  brightMagenta: "#cdb9f0",
  cyan: "#7fc7d6",
  brightCyan: "#a3dbe6",
  white: "#c6cbd6",
  brightWhite: "#eef1f6",
};

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
      theme: PUBLIC_TERM_THEME,
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
    const selectionDisposable = term.onSelectionChange(() => {
      const text = term.getSelection();
      if (!text) return;
      navigator.clipboard?.writeText(text).then(
        () => onSelectionCopyRef.current?.(text, null),
        (error) => onSelectionCopyRef.current?.("", error),
      );
    });

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
      term.dispose();
      termRef.current = null;
    };
  }, []);

  return <div ref={hostRef} className="h-full w-full min-h-0" />;
}
