import { useEffect, useState, type CSSProperties, type DragEvent as ReactDragEvent } from "react";
import { dropPreviewRect, dropRegionAt, type DropRegion } from "../tabDropZone";
import { endTabDrag, tabDragFrom, useTabDrag } from "./tabDrag";

const REGION_LABEL: Record<DropRegion, string> = {
  left: "放下以在左侧新建窗格",
  right: "放下以在右侧新建窗格",
  top: "放下以在上方新建窗格",
  bottom: "放下以在下方新建窗格",
  center: "放下以移到这个窗格",
};

function percent(value: number): string {
  return `${value * 100}%`;
}

export interface TabDropPane {
  paneId: string;
  /** The pane's content area, where the drop target sits. */
  style: CSSProperties;
}

/**
 * Drop targets for a dragged tab, rendered only while a tab drag is active: one over each shown pane's
 * content, so terminals keep their own mouse handling the rest of the time. Edges split the pane and
 * the centre moves the tab there. The source pane offers no centre (the tab is already there), and no
 * edges either when the tab is its only one, so a pane is never left empty by splitting itself.
 */
export function TabDropLayer({
  panes,
  edges,
  onTabDrop,
}: {
  panes: TabDropPane[];
  /** Offer edge splits; false in single-pane layouts, where only moves are possible. */
  edges: boolean;
  onTabDrop: (tabId: string, paneId: string, region: DropRegion) => void;
}) {
  const drag = useTabDrag();
  const [hover, setHover] = useState<{ paneId: string; region: DropRegion } | null>(null);
  useEffect(() => {
    if (!drag) setHover(null);
  }, [drag]);
  if (!drag) return null;

  const hitFor = (event: ReactDragEvent<HTMLDivElement>, paneId: string): { tabId: string; region: DropRegion } | null => {
    const source = tabDragFrom(event.dataTransfer);
    if (!source) return null;
    const own = source.paneId === paneId;
    const region = dropRegionAt(
      { x: event.clientX, y: event.clientY },
      event.currentTarget.getBoundingClientRect(),
      { edges: edges && !(own && source.paneTabCount < 2) },
    );
    if (!region || (own && region === "center")) return null;
    return { tabId: source.tabId, region };
  };
  const clear = (paneId: string) => setHover((current) => (current?.paneId === paneId ? null : current));

  return (
    <>
      {panes.map(({ paneId, style }) => {
        const region = hover?.paneId === paneId ? hover.region : null;
        const box = region ? dropPreviewRect(region) : null;
        return (
          <div
            key={paneId}
            data-tab-drop-pane={paneId}
            className="nx-tab-drop-target"
            style={style}
            onDragOver={(event) => {
              const hit = hitFor(event, paneId);
              if (!hit) {
                clear(paneId);
                return;
              }
              event.preventDefault();
              event.dataTransfer.dropEffect = "move";
              setHover((current) =>
                current?.paneId === paneId && current.region === hit.region ? current : { paneId, region: hit.region },
              );
            }}
            onDragLeave={(event) => {
              if (event.relatedTarget instanceof Node && event.currentTarget.contains(event.relatedTarget)) return;
              clear(paneId);
            }}
            onDrop={(event) => {
              const hit = hitFor(event, paneId);
              if (!hit) return;
              event.preventDefault();
              setHover(null);
              endTabDrag();
              onTabDrop(hit.tabId, paneId, hit.region);
            }}
          >
            {region && box && (
              <div
                className="nx-tab-drop-preview"
                data-drop-region={region}
                role="status"
                aria-label={REGION_LABEL[region]}
                style={{ left: percent(box.x), top: percent(box.y), width: percent(box.width), height: percent(box.height) }}
              />
            )}
          </div>
        );
      })}
    </>
  );
}
