import { useEffect, useRef, useState, type CSSProperties, type ReactNode, type RefObject } from "react";
import { clampSplitRatio, splitRatioAt, splitRatioForKey } from "../../features/terminal/workspaceLayout";
import { createFrameCoalescer, RESIZE_END_EVENT, type FrameCoalescer } from "../../ui/ResizeHandle";
import type { Pane, Workspace } from "../store";
import { workspaceLayoutTree } from "../paneLayout";
import { paneRects, resolveActivePane, type Rect, type SplitDir, type SplitNode } from "../splitTree";

// Divider thickness in pixels; each pane gives up half of it on every inner edge.
export const SPLIT_HANDLE_SIZE = 6;
const HALF = SPLIT_HANDLE_SIZE / 2;
const EDGE = 1e-9;

export interface SplitHandleLayout {
  id: string;
  dir: SplitDir;
  ratio: number;
  /** The split node's own area, in fractions of the workspace. */
  rect: Rect;
}

/** Every split node of the tree with the area it divides, outermost first. */
export function splitHandles(tree: SplitNode): SplitHandleLayout[] {
  const out: SplitHandleLayout[] = [];
  const walk = (node: SplitNode, rect: Rect) => {
    if (node.type === "leaf") return;
    out.push({ id: node.id, dir: node.dir, ratio: node.ratio, rect });
    if (node.dir === "row") {
      const width = rect.width * node.ratio;
      walk(node.a, { ...rect, width });
      walk(node.b, { ...rect, x: rect.x + width, width: rect.width - width });
    } else {
      const height = rect.height * node.ratio;
      walk(node.a, { ...rect, height });
      walk(node.b, { ...rect, y: rect.y + height, height: rect.height - height });
    }
  };
  walk(tree, { x: 0, y: 0, width: 1, height: 1 });
  return out;
}

function percent(value: number): string {
  return `${value * 100}%`;
}

function edgeOffset(start: number, size: number): { before: number; after: number } {
  return { before: start > EDGE ? HALF : 0, after: start + size < 1 - EDGE ? HALF : 0 };
}

/** Absolute placement of a pane: its rectangle minus half a divider on each inner edge. */
export function paneStyle(rect: Rect): CSSProperties {
  const x = edgeOffset(rect.x, rect.width);
  const y = edgeOffset(rect.y, rect.height);
  return {
    left: `calc(${percent(rect.x)} + ${x.before}px)`,
    top: `calc(${percent(rect.y)} + ${y.before}px)`,
    width: `calc(${percent(rect.width)} - ${x.before + x.after}px)`,
    height: `calc(${percent(rect.height)} - ${y.before + y.after}px)`,
  };
}

function handleStyle(handle: SplitHandleLayout): CSSProperties {
  const { rect, ratio } = handle;
  const ratioOf = clampSplitRatio(ratio);
  if (handle.dir === "row") {
    return {
      left: `calc(${percent(rect.x + rect.width * ratioOf)} - ${HALF}px)`,
      top: percent(rect.y),
      width: `${SPLIT_HANDLE_SIZE}px`,
      height: percent(rect.height),
    };
  }
  return {
    left: percent(rect.x),
    top: `calc(${percent(rect.y + rect.height * ratioOf)} - ${HALF}px)`,
    width: percent(rect.width),
    height: `${SPLIT_HANDLE_SIZE}px`,
  };
}

interface SplitViewProps {
  workspace: Workspace;
  /** Show only the active pane, full size (mobile and compact layouts). Hidden panes stay mounted. */
  singlePane: boolean;
  renderPane: (pane: Pane) => ReactNode;
  onRatio: (splitId: string, ratio: number) => void;
}

/**
 * Lays out a workspace's split tree. Panes render as keyed siblings of one container and are placed
 * absolutely from the tree, so splitting, closing, resizing or switching to the single-pane view
 * never re-parents a pane: its terminal keeps its DOM node and connection.
 */
export function SplitView({ workspace, singlePane, renderPane, onRatio }: SplitViewProps) {
  const boxRef = useRef<HTMLDivElement>(null);
  const tree = workspaceLayoutTree(workspace);
  const rects = new Map((tree ? paneRects(tree) : []).map((entry) => [entry.paneId, entry.rect]));
  const activePaneId = tree ? resolveActivePane(tree, workspace.activePaneId) : workspace.activePaneId;
  const handles = tree && !singlePane ? splitHandles(tree) : [];

  return (
    <div ref={boxRef} className="nx-split-view relative h-full min-h-0">
      {workspace.panes.map((pane) => {
        const rect = rects.get(pane.id);
        const shown = singlePane ? pane.id === activePaneId : rect !== undefined;
        return (
          <div
            key={pane.id}
            data-pane-id={pane.id}
            className={shown ? "absolute flex min-h-0 min-w-0 flex-col" : "hidden"}
            style={shown ? (singlePane || !rect ? { inset: 0 } : paneStyle(rect)) : undefined}
          >
            {renderPane(pane)}
          </div>
        );
      })}
      {handles.map((handle) => (
        <SplitHandle key={handle.id} handle={handle} boxRef={boxRef} onRatio={onRatio} />
      ))}
    </div>
  );
}

function SplitHandle({
  handle,
  boxRef,
  onRatio,
}: {
  handle: SplitHandleLayout;
  boxRef: RefObject<HTMLDivElement | null>;
  onRatio: (splitId: string, ratio: number) => void;
}) {
  const dragging = useRef(false);
  const [active, setActive] = useState(false);
  const currentRatio = clampSplitRatio(handle.ratio);
  const ratioRef = useRef(currentRatio);
  ratioRef.current = currentRatio;
  const latest = useRef({ handle, onRatio });
  latest.current = { handle, onRatio };
  const framesRef = useRef<FrameCoalescer<number> | null>(null);
  if (!framesRef.current) {
    framesRef.current = createFrameCoalescer<number>((next) => latest.current.onRatio(latest.current.handle.id, next));
  }

  useEffect(() => () => framesRef.current?.cancel(), []);

  const row = handle.dir === "row";
  const measure = (clientX: number, clientY: number) => {
    const box = boxRef.current?.getBoundingClientRect();
    if (!box) return ratioRef.current;
    const { rect } = latest.current.handle;
    // The split's own area in pixels; the pointer's share of it is the new ratio.
    return row
      ? splitRatioAt(clientX, { top: box.left + rect.x * box.width, height: rect.width * box.width }, ratioRef.current)
      : splitRatioAt(clientY, { top: box.top + rect.y * box.height, height: rect.height * box.height }, ratioRef.current);
  };
  const finish = (point?: { clientX: number; clientY: number }) => {
    if (!dragging.current) return;
    dragging.current = false;
    setActive(false);
    if (point === undefined) framesRef.current?.flush();
    else framesRef.current?.flush(measure(point.clientX, point.clientY));
    window.dispatchEvent(new Event(RESIZE_END_EVENT));
  };
  const share = Math.round(currentRatio * 100);
  const first = row ? "左栏" : "上栏";

  return (
    <div
      role="separator"
      tabIndex={0}
      aria-orientation={row ? "vertical" : "horizontal"}
      aria-label={row ? "调整左右分屏比例" : "调整上下分屏比例"}
      aria-valuemin={15}
      aria-valuemax={85}
      aria-valuenow={share}
      aria-valuetext={`${first} ${share}%`}
      data-split-id={handle.id}
      className={`nx-split-handle ${row ? "is-row" : "is-col"} ${active ? "is-dragging" : ""}`}
      style={{ position: "absolute", ...handleStyle(handle) }}
      title={`拖动调整${row ? "左右" : "上下"}比例（双击或回车复位，方向键微调）`}
      onPointerDown={(event) => {
        if (event.button !== 0) return;
        event.preventDefault();
        event.currentTarget.setPointerCapture(event.pointerId);
        dragging.current = true;
        setActive(true);
      }}
      onPointerMove={(event) => {
        if (dragging.current) framesRef.current?.schedule(measure(event.clientX, event.clientY));
      }}
      onPointerUp={(event) => {
        finish({ clientX: event.clientX, clientY: event.clientY });
        if (event.currentTarget.hasPointerCapture(event.pointerId)) {
          event.currentTarget.releasePointerCapture(event.pointerId);
        }
      }}
      onPointerCancel={() => finish()}
      onLostPointerCapture={() => finish()}
      onDoubleClick={() => {
        framesRef.current?.cancel();
        latest.current.onRatio(handle.id, 0.5);
      }}
      onKeyDown={(event) => {
        const next = splitRatioForKey(ratioRef.current, event.key, event.shiftKey, handle.dir);
        if (next === null) return;
        event.preventDefault();
        latest.current.onRatio(handle.id, next);
      }}
    >
      <span className="nx-split-grip" />
    </div>
  );
}
