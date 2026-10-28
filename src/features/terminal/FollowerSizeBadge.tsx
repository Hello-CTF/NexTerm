import { useId, useState } from "react";
import { followerReadable, type FollowerScale, type FollowerZoom } from "./followerScale";

export interface FollowerSizeBadgeProps {
  // How the view scales the PTY grid; null until it measured.
  scale: FollowerScale | null;
  // Another device owns the size (rather than nobody, briefly, between owners).
  ownedElsewhere: boolean;
  zoom: FollowerZoom;
  onZoom: (zoom: FollowerZoom) => void;
}

// FollowerSizeBadge is a compact chip on a terminal that renders the PTY size another device set: "跟随 · 62%" and a
// zoom toggle. The chip expands to explain that the PTY follows the device that typed last, so typing here takes the
// size; there is no other way to take it. The zoom switches between the whole grid and a larger font that fits the
// rows and pans the columns (a pinch on the terminal does the same).
export function FollowerSizeBadge({ scale, ownedElsewhere, zoom, onZoom }: FollowerSizeBadgeProps) {
  const id = useId();
  // A pinch is only suggested where a touch screen is the primary pointer.
  const touch = typeof window.matchMedia === "function" && window.matchMedia("(pointer: coarse)").matches;
  const [expanded, setExpanded] = useState(false);
  const percent = scale && scale.ratio < 1 ? Math.round(scale.ratio * 100) : null;
  const zoomable = zoom === "height" || percent !== null;
  const small = scale !== null && (scale.panY || (zoom === "fit" && !followerReadable(scale)));
  const state = ownedElsewhere ? "终端尺寸跟随另一台设备" : "终端按当前尺寸显示";
  const spoken = percent === null ? state : `${state}，缩放到 ${percent}%`;
  return (
    <div className="pointer-events-none flex max-w-full flex-col items-end gap-0.5" data-follower-size="" data-follower-zoom={zoom}>
      {/* Announced once when the view starts following; the chip's own label carries the scale. */}
      <span role="status" className="sr-only">
        {state}
      </span>
      <div className="flex items-center gap-1 rounded border border-neutral-700/70 bg-neutral-900/85 px-1 py-0.5 text-[11px] text-neutral-300 shadow">
        <button
          type="button"
          className="pointer-events-auto rounded px-1 pointer-coarse:min-h-8"
          aria-label={spoken}
          aria-expanded={expanded}
          aria-controls={`${id}-why`}
          title={spoken}
          data-follower-chip=""
          onClick={() => setExpanded((value) => !value)}
        >
          跟随{percent !== null && ` · ${percent}%`}
        </button>
        {zoomable && (
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm pointer-events-auto pointer-coarse:min-h-8 px-1.5 py-0 text-[11px]"
            aria-pressed={zoom === "height"}
            title={zoom === "height" ? "再按一次显示整个终端" : "放大文字，左右滑动查看"}
            data-follower-zoom-toggle=""
            onClick={() => onZoom(zoom === "height" ? "fit" : "height")}
          >
            放大文字
          </button>
        )}
      </div>
      {expanded && (
        <p
          id={`${id}-why`}
          className="pointer-events-auto max-w-[320px] rounded bg-neutral-900/90 px-2 py-1 text-right text-[11px] text-neutral-300"
          data-follower-why=""
        >
          终端尺寸跟随最后输入的设备。在此输入即改用本设备尺寸，另一台设备随后按比例显示。
        </p>
      )}
      {small && (
        <span className="rounded bg-neutral-900/85 px-2 py-0.5 text-[11px] text-amber-300" data-follower-small="">
          {scale?.panY ? "文字已缩到最小，终端仍显示不全：上下滑动查看" : touch ? "文字较小：双指张开或点「放大文字」" : "文字较小：点「放大文字」"}
        </span>
      )}
    </div>
  );
}
