import { useCallback, useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";

// A selection is a set of paths plus the anchor a Shift range starts from and the primary item that
// the rest of the panel (details, logs, persisted view) treats as "the" selected path.
export interface SelectionState {
  primary: string | null;
  anchor: string | null;
  paths: ReadonlySet<string>;
}

const EMPTY_SELECTION: SelectionState = { primary: null, anchor: null, paths: new Set() };

const LONG_PRESS_MS = 500;
const LONG_PRESS_SLOP = 10;

function without(cur: SelectionState, gone: ReadonlySet<string>): SelectionState {
  const kept = [...cur.paths].filter((p) => !gone.has(p));
  if (kept.length === cur.paths.size) return cur;
  if (kept.length === 0) return EMPTY_SELECTION;
  return {
    primary: cur.primary !== null && !gone.has(cur.primary) ? cur.primary : kept[kept.length - 1],
    anchor: cur.anchor !== null && !gone.has(cur.anchor) ? cur.anchor : kept[0],
    paths: new Set(kept),
  };
}

export function useMultiSelect(initialPrimary: string | null = null) {
  const [sel, setSel] = useState<SelectionState>(() =>
    initialPrimary === null
      ? EMPTY_SELECTION
      : { primary: initialPrimary, anchor: initialPrimary, paths: new Set([initialPrimary]) },
  );
  const [multiMode, setMultiMode] = useState(false);

  const setSelected = useCallback((next: string | null) => {
    setSel(next === null ? EMPTY_SELECTION : { primary: next, anchor: next, paths: new Set([next]) });
  }, []);

  const toggle = useCallback((path: string) => {
    setSel((cur) => {
      const paths = new Set(cur.paths);
      if (paths.has(path)) {
        paths.delete(path);
        if (paths.size === 0) return EMPTY_SELECTION;
        const primary = cur.primary === path ? [...paths][paths.size - 1] : cur.primary;
        return { primary, anchor: path, paths };
      }
      paths.add(path);
      return { primary: path, anchor: path, paths };
    });
  }, []);

  // order is the visible row order; the range runs from the anchor to path.
  const range = useCallback((path: string, order: readonly string[]) => {
    setSel((cur) => {
      const to = order.indexOf(path);
      const fromPath = cur.anchor ?? cur.primary;
      const from = fromPath === null ? -1 : order.indexOf(fromPath);
      if (to < 0 || from < 0) return { primary: path, anchor: path, paths: new Set([path]) };
      const [lo, hi] = from <= to ? [from, to] : [to, from];
      return { primary: path, anchor: order[from], paths: new Set(order.slice(lo, hi + 1)) };
    });
  }, []);

  const selectAll = useCallback((order: readonly string[]) => {
    if (order.length === 0) return;
    setSel((cur) => ({
      primary: cur.primary !== null && order.includes(cur.primary) ? cur.primary : order[0],
      anchor: order[0],
      paths: new Set(order),
    }));
  }, []);

  const clear = useCallback(() => {
    setSel(EMPTY_SELECTION);
    setMultiMode(false);
  }, []);

  const drop = useCallback((gone: readonly string[]) => {
    if (gone.length > 0) setSel((cur) => without(cur, new Set(gone)));
  }, []);

  // Forget selected paths that are no longer among the live ones.
  const prune = useCallback((live: ReadonlySet<string>) => {
    setSel((cur) => {
      if (cur.paths.size === 0) return cur;
      return without(cur, new Set([...cur.paths].filter((p) => !live.has(p))));
    });
  }, []);

  const remap = useCallback((map: (path: string) => string) => {
    setSel((cur) => {
      if (cur.paths.size === 0) return cur;
      return {
        primary: cur.primary === null ? null : map(cur.primary),
        anchor: cur.anchor === null ? null : map(cur.anchor),
        paths: new Set([...cur.paths].map(map)),
      };
    });
  }, []);

  const enterMulti = useCallback((path: string) => {
    setMultiMode(true);
    setSel((cur) => {
      const paths = new Set(cur.paths);
      paths.add(path);
      return { primary: path, anchor: path, paths };
    });
  }, []);

  useEffect(() => {
    if (multiMode && sel.paths.size === 0) setMultiMode(false);
  }, [multiMode, sel.paths.size]);

  return { sel, multiMode, setSelected, toggle, range, selectAll, clear, drop, prune, remap, enterMulti };
}

// Touch long press: fires after LONG_PRESS_MS without moving more than LONG_PRESS_SLOP, then swallows the click that ends the press.
export function useLongPress(onLongPress: (id: string) => void) {
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const origin = useRef<{ x: number; y: number } | null>(null);
  const suppress = useRef(false);
  const handler = useRef(onLongPress);
  handler.current = onLongPress;

  const cancel = useCallback(() => {
    if (timer.current !== null) clearTimeout(timer.current);
    timer.current = null;
    origin.current = null;
  }, []);
  useEffect(() => cancel, [cancel]);

  const start = useCallback(
    (ev: ReactPointerEvent<HTMLElement>, id: string) => {
      suppress.current = false;
      if (ev.pointerType !== "touch" && ev.pointerType !== "pen") return;
      if ((ev.target as HTMLElement).closest("button")) return;
      cancel();
      origin.current = { x: ev.clientX, y: ev.clientY };
      timer.current = setTimeout(() => {
        timer.current = null;
        origin.current = null;
        suppress.current = true;
        handler.current(id);
      }, LONG_PRESS_MS);
    },
    [cancel],
  );

  const move = useCallback(
    (ev: ReactPointerEvent<HTMLElement>) => {
      const from = origin.current;
      if (from && (Math.abs(ev.clientX - from.x) > LONG_PRESS_SLOP || Math.abs(ev.clientY - from.y) > LONG_PRESS_SLOP)) {
        cancel();
      }
    },
    [cancel],
  );

  // True once after a long press, so the click that releases it is ignored.
  const consumeClick = useCallback(() => {
    const was = suppress.current;
    suppress.current = false;
    return was;
  }, []);

  return { start, move, cancel, consumeClick };
}
