import { followerReadable, type FollowerScale, type FollowerZoom } from "./followerScale";

export interface FollowerSizeBadgeProps {
  // How the view scales the PTY grid; null until it measured.
  scale: FollowerScale | null;
  // Another device owns the size (rather than nobody, briefly, between owners).
  ownedElsewhere: boolean;
  zoom: FollowerZoom;
  onZoom: (zoom: FollowerZoom) => void;
}

// FollowerSizeBadge marks a terminal that renders the PTY size another device set. The PTY follows the device that
// typed last, so typing here takes the size; there is no other way to take it. The zoom button (and a pinch on the
// terminal) switches between the whole grid and a larger font that fits the rows and pans the columns.
export function FollowerSizeBadge({ scale, ownedElsewhere, zoom, onZoom }: FollowerSizeBadgeProps) {
  const percent = scale && scale.ratio < 1 ? Math.round(scale.ratio * 100) : null;
  const zoomable = zoom === "height" || percent !== null;
  const small = scale !== null && zoom === "fit" && !followerReadable(scale);
  return (
    <div
      className="pointer-events-none flex max-w-full flex-col items-end gap-0.5"
      data-follower-size=""
      data-follower-zoom={zoom}
    >
      <div className="flex items-center gap-1.5 rounded border border-neutral-700/70 bg-neutral-900/85 px-2 py-0.5 text-[11px] text-neutral-300 shadow">
        <span title="终端尺寸跟随最后输入的设备；在这里输入后改用本设备的尺寸">
          {ownedElsewhere ? "尺寸跟随另一台设备" : "按终端当前尺寸显示"}
          {percent !== null && ` · ${percent}%`}
          <span className="text-[var(--nx-fg-soft)]"> · 在此输入即改用本设备尺寸</span>
        </span>
        {zoomable && (
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm pointer-events-auto pointer-coarse:min-h-8 px-1.5 py-0 text-[11px]"
            aria-pressed={zoom === "height"}
            title={zoom === "height" ? "显示整个终端" : "放大文字，左右滑动查看"}
            onClick={() => onZoom(zoom === "height" ? "fit" : "height")}
          >
            {zoom === "height" ? "显示全部" : "放大"}
          </button>
        )}
      </div>
      {small && (
        <span className="rounded bg-neutral-900/85 px-2 py-0.5 text-[11px] text-amber-300" data-follower-small="">
          文字较小：双指张开或点「放大」查看
        </span>
      )}
    </div>
  );
}
