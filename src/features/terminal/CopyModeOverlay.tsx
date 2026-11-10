import { useEffect, useReducer, useRef, useState, useSyncExternalStore, type KeyboardEvent as ReactKeyboardEvent } from "react";
import type { Terminal } from "@xterm/xterm";
import type { CopyModeController } from "./copyMode";
import { t } from "../../i18n";

interface CellGeometry {
  left: number;
  top: number;
  width: number;
  height: number;
}

// Cell geometry from xterm's screen element: the same box both the DOM and the WebGL renderer draw into.
function cellGeometry(term: Terminal, container: HTMLElement): CellGeometry | null {
  const screen = term.element?.querySelector<HTMLElement>(".xterm-screen");
  if (!screen || term.cols <= 0 || term.rows <= 0) return null;
  const box = screen.getBoundingClientRect();
  const origin = container.getBoundingClientRect();
  if (box.width <= 0 || box.height <= 0) return null;
  return { left: box.left - origin.left, top: box.top - origin.top, width: box.width / term.cols, height: box.height / term.rows };
}

function SearchPrompt({ direction, onSubmit, onCancel }: { direction: "forward" | "backward"; onSubmit: (q: string) => void; onCancel: () => void }) {
  const [query, setQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  useEffect(() => inputRef.current?.focus(), []);
  const onKeyDown = (event: ReactKeyboardEvent<HTMLInputElement>) => {
    event.stopPropagation();
    if (event.nativeEvent.isComposing) return;
    if (event.key === "Enter") {
      event.preventDefault();
      onSubmit(query);
    } else if (event.key === "Escape") {
      event.preventDefault();
      onCancel();
    }
  };
  return (
    <label className="flex items-center gap-1">
      <span aria-hidden="true">{direction === "forward" ? "/" : "?"}</span>
      <input
        ref={inputRef}
        className="nx-input nx-input-sm w-48"
        aria-label={direction === "forward" ? t("向下搜索") : t("向上搜索")}
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        onKeyDown={onKeyDown}
        onBlur={onCancel}
        spellCheck={false}
        autoComplete="off"
      />
    </label>
  );
}

/** The copy mode cursor, block selection and status hint drawn over the terminal while copy mode is active. */
export function CopyModeOverlay({ controller, term }: { controller: CopyModeController; term: Terminal }) {
  const state = useSyncExternalStore(controller.subscribe, controller.state);
  const [, redraw] = useReducer((n: number) => n + 1, 0);
  // A callback ref into state: the first commit re-renders once with the geometry available.
  const [root, setRoot] = useState<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!state.active) return;
    const subs = [term.onScroll(redraw), term.onResize(redraw), term.onRender(redraw)];
    return () => subs.forEach((sub) => sub.dispose());
  }, [state.active, term]);
  if (!state.active) return null;

  // The overlay fills the same box as the terminal host, so its own rectangle is the origin for cell positions.
  const geometry = root ? cellGeometry(term, root) : null;
  const top = term.buffer.active.viewportY;
  const visible = (y: number) => y >= top && y < top + term.rows;
  const cursorWidth = Math.max(1, term.buffer.active.getLine(state.cursor.y)?.getCell(state.cursor.x)?.getWidth() ?? 1);
  const hint = state.message ?? t("复制模式 · v 选择 · y 复制 · Esc 退出");

  return (
    <div ref={setRoot} className="pointer-events-none absolute inset-0 z-10" data-testid="copy-mode">
      {geometry &&
        controller.blockRects().filter((rect) => visible(rect.y)).map((rect) => (
          <div
            key={rect.y}
            className="absolute bg-[color-mix(in_srgb,var(--color-accent)_35%,transparent)]"
            style={{
              left: geometry.left + rect.x0 * geometry.width,
              top: geometry.top + (rect.y - top) * geometry.height,
              width: (rect.x1 - rect.x0) * geometry.width,
              height: geometry.height,
            }}
          />
        ))}
      {geometry && visible(state.cursor.y) && (
        <div
          data-testid="copy-mode-cursor"
          className="absolute border-2 border-[var(--color-accent)] bg-[color-mix(in_srgb,var(--color-accent)_25%,transparent)]"
          style={{
            left: geometry.left + state.cursor.x * geometry.width,
            top: geometry.top + (state.cursor.y - top) * geometry.height,
            width: cursorWidth * geometry.width,
            height: geometry.height,
          }}
        />
      )}
      <div
        role="status"
        className="pointer-events-auto absolute bottom-1.5 left-3 flex items-center gap-2 rounded border border-neutral-700/80 bg-neutral-800/95 px-2 py-1 text-xs text-neutral-100 shadow-lg"
      >
        {state.prompt ? (
          <SearchPrompt direction={state.prompt.direction} onSubmit={controller.submitSearch} onCancel={controller.cancelSearch} />
        ) : (
          <span>{hint}</span>
        )}
      </div>
    </div>
  );
}
