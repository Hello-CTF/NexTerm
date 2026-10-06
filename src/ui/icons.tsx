// 图标系统：一套 24×24 线性图标，`currentColor` 描边，统一 1.7 线宽。
//
// 为什么自己内联而不用图标库：项目要求依赖可控（技术选型「低依赖」），
// 而这套图标只需要一种尺寸、单色描边，内联反而更小、更可控、更好统一。
//
// 用法：<IconTerminal size={14} className="text-neutral-400" />
import type { ReactNode, SVGProps } from "react";

export interface IconProps extends Omit<SVGProps<SVGSVGElement>, "children"> {
  /** 边长（px），默认 16。 */
  size?: number;
}

function Svg({ size = 16, children, ...rest }: IconProps & { children: ReactNode }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.7}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      {...rest}
    >
      {children}
    </svg>
  );
}

/* ── 品牌 ──────────────────────────────────────────────────────────────── */

/**
 * 应用标识（霓虹猫娘 · 透明底）。
 *
 * 直接引用位图，而不是内联 SVG —— 这一版是带辉光和渐变的栅格画，
 * 没有任何矢量版本，硬描成 SVG 只会丢掉它最值钱的那层辉光。
 *
 * 资产来源与规格：
 *   `public/brand/nexterm-mark-128.png` ← 品牌图 1.375x 局部裁切（脸 + 耳机 + 终端）
 *   128px 是给 2x 屏上 52px 空态用的；图标栏 26px 也走它，缩放质量足够。
 *   应用图标（任务栏 / 安装包）由 1024 母版 `src-tauri/icons/source.png`
 *   经 `pnpm tauri icon` 生成，见 `src-tauri/icons/`。
 *
 * 背景是**纯透明**，不是早期的 `#0B0E14` 实底方砖 —— 实底会在 Dock / 任务栏上
 * 显出一个黑方块。品牌标刻意比应用图标裁得紧：全幅缩到 26px 只剩一团，
 * 深色侧栏（`--color-neutral-950`）上连轮廓都糊掉；同时仍保留透明边，
 * 否则裁成满幅又变成一个「方砖」。
 *
 * 换方案：把 `public/brand/` 换成新图、`src-tauri/icons/source.png` 换成 1024 母版，
 * 再跑一次 `pnpm tauri icon`。
 */
export function Logo({ size = 26 }: { size?: number }) {
  return (
    <img
      src={`${import.meta.env.BASE_URL}brand/nexterm-mark-128.png`}
      width={size}
      height={size}
      alt="NexTerm"
      draggable={false}
      className="shrink-0 select-none"
    />
  );
}

/* ── 资产类型 ──────────────────────────────────────────────────────────── */

export const IconServer = (p: IconProps) => (
  <Svg {...p}>
    <rect x="2.5" y="3.5" width="19" height="7" rx="2" />
    <rect x="2.5" y="13.5" width="19" height="7" rx="2" />
    <path d="M6.5 7h.01M6.5 17h.01" />
    <path d="M11 7h5M11 17h5" />
  </Svg>
);

export const IconTerminal = (p: IconProps) => (
  <Svg {...p}>
    <path d="m4.5 16.5 5.5-5-5.5-5" />
    <path d="M12.5 19h7" />
  </Svg>
);

export const IconMonitor = (p: IconProps) => (
  <Svg {...p}>
    <rect x="2.5" y="3.5" width="19" height="13" rx="2" />
    <path d="M8.5 20.5h7M12 16.5v4" />
  </Svg>
);

/** Windows / WinRM 目标：四宫格。 */
export const IconGrid = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3" y="3" width="7.5" height="7.5" rx="1.8" />
    <rect x="13.5" y="3" width="7.5" height="7.5" rx="1.8" />
    <rect x="3" y="13.5" width="7.5" height="7.5" rx="1.8" />
    <rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.8" />
  </Svg>
);

export const IconFolder = (p: IconProps) => (
  <Svg {...p}>
    <path d="M3.5 6.5A2 2 0 0 1 5.5 4.5h3.3a2 2 0 0 1 1.6.8l1 1.4h7.1a2 2 0 0 1 2 2v8.3a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z" />
  </Svg>
);

export const IconFolderOpen = (p: IconProps) => (
  <Svg {...p}>
    <path d="m6 14.5 1.45-2.9A2 2 0 0 1 9.24 10.5H20a2 2 0 0 1 1.94 2.5l-1.55 6a2 2 0 0 1-1.94 1.5H4a2 2 0 0 1-2-2v-14a2 2 0 0 1 2-2h3.93a2 2 0 0 1 1.66.9l.82 1.2a2 2 0 0 0 1.66.9H18a2 2 0 0 1 2 2v1.5" />
  </Svg>
);

export const IconFile = (p: IconProps) => (
  <Svg {...p}>
    <path d="M15 2.5H6.5a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2h11a2 2 0 0 0 2-2V7z" />
    <path d="M14 2.5V7a1 1 0 0 0 1 1h4.5" />
    <path d="M9 13h6M9 17h4" />
  </Svg>
);

/* ── 文件树 / 编辑器用到的文件类型图标 ─────────────────────────────────── */

/** 新建文件：文件轮廓内嵌加号。 */
export const IconFilePlus = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12.5 2.5H6.5a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2h11a2 2 0 0 0 2-2V9.5z" />
    <path d="M12.5 2.5v6a1 1 0 0 0 1 1h6" />
    <path d="M9.6 16.6h4.8M12 14.2v4.8" />
  </Svg>
);

/** 新建目录：文件夹轮廓内嵌加号。 */
export const IconFolderPlus = (p: IconProps) => (
  <Svg {...p}>
    <path d="M3.5 6.5A2 2 0 0 1 5.5 4.5h3.3a2 2 0 0 1 1.6.8l1 1.4h7.1a2 2 0 0 1 2 2v8.3a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z" />
    <path d="M9.6 14h4.8M12 11.6v4.8" />
  </Svg>
);

/** 源码文件。 */
export const IconCode = (p: IconProps) => (
  <Svg {...p}>
    <path d="m8.5 7.5-5 4.5 5 4.5" />
    <path d="m15.5 7.5 5 4.5-5 4.5" />
  </Svg>
);

/** 压缩包 / 归档。 */
export const IconArchive = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3" y="4" width="18" height="4.4" rx="1.5" />
    <path d="M4.6 8.4V18a2 2 0 0 0 2 2h10.8a2 2 0 0 0 2-2V8.4" />
    <path d="M10 12.6h4" />
  </Svg>
);

/** 图片文件。 */
export const IconImage = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3.5" y="4.5" width="17" height="15" rx="2.5" />
    <circle cx="9" cy="10" r="1.5" />
    <path d="m4.6 17.6 4.2-4.2a2 2 0 0 1 2.8 0l6.6 6.6" />
  </Svg>
);

/** 家目录（路径面包屑的「回到 ~」按钮）。 */
export const IconHome = (p: IconProps) => (
  <Svg {...p}>
    <path d="M4 10.4 12 4l8 6.4V19a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2z" />
    <path d="M9.6 21v-5.6h4.8V21" />
  </Svg>
);

/** 折叠全部：顶部一条基线 + 双层上箭头。 */
export const IconFoldAll = (p: IconProps) => (
  <Svg {...p}>
    <path d="M4 4.5h16" />
    <path d="m7 11.5 5-5 5 5" />
    <path d="m7 19 5-5 5 5" />
  </Svg>
);

/** 撤销 / 重做：一对方向相反的弧形箭头（别用同一个图标翻转，线帽会走形）。 */
export const IconUndo = (p: IconProps) => (
  <Svg {...p}>
    <path d="M9 14.5 4 9.5l5-5" />
    <path d="M4 9.5h10.5a5.5 5.5 0 0 1 0 11H9.5" />
  </Svg>
);

export const IconRedo = (p: IconProps) => (
  <Svg {...p}>
    <path d="m15 14.5 5-5-5-5" />
    <path d="M20 9.5H9.5a5.5 5.5 0 0 0 0 11h5" />
  </Svg>
);

/** 减号。 */
export const IconMinus = (p: IconProps) => (
  <Svg {...p}>
    <path d="M5 12h14" />
  </Svg>
);

/** 最大化 / 还原（窗口控制钮）。 */
export const IconMaximize = (p: IconProps) => (
  <Svg {...p}>
    <rect x="5" y="5" width="14" height="14" rx="2" />
  </Svg>
);

/* ── 分屏 ─────────────────────────────────────────────────────────────── */

/** 上下分屏：外框 + 一条横向分隔线。 */
export const IconSplitH = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3" y="4" width="18" height="16" rx="2.5" />
    <path d="M3 12h18" />
  </Svg>
);

/** 左右分屏：外框 + 一条纵向分隔线（与 IconSplitH 同尺寸约定，只把线转 90°）。 */
export const IconSplitV = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3" y="4" width="18" height="16" rx="2.5" />
    <path d="M12 4v16" />
  </Svg>
);

/** 合并（取消分屏）：两个箭头对着中间那条线收拢。 */
export const IconMergeH = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3" y="4" width="18" height="16" rx="2.5" />
    <path d="M3 12h18" />
    <path d="m7 9 3 3-3 3" />
    <path d="m17 9-3 3 3 3" />
  </Svg>
);

/** 容器 / Docker。 */
export const IconBox = (p: IconProps) => (
  <Svg {...p}>
    <path d="m21 7.6-9-5.1-9 5.1v8.8l9 5.1 9-5.1z" />
    <path d="m3 7.6 9 5.1 9-5.1" />
    <path d="M12 21.5v-8.8" />
  </Svg>
);

export const IconDatabase = (p: IconProps) => (
  <Svg {...p}>
    <ellipse cx="12" cy="5.5" rx="7.5" ry="2.8" />
    <path d="M4.5 5.5v13c0 1.55 3.36 2.8 7.5 2.8s7.5-1.25 7.5-2.8v-13" />
    <path d="M4.5 12c0 1.55 3.36 2.8 7.5 2.8s7.5-1.25 7.5-2.8" />
  </Svg>
);

/** Redis / 多类型存储。 */
export const IconLayers = (p: IconProps) => (
  <Svg {...p}>
    <path d="m12 2.8 8.5 4.6-8.5 4.6-8.5-4.6z" />
    <path d="m3.5 12.4 8.5 4.6 8.5-4.6" />
    <path d="m3.5 16.9 8.5 4.6 8.5-4.6" />
  </Svg>
);

export const IconGlobe = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M3.2 9.5h17.6M3.2 14.5h17.6" />
    <path d="M12 3a15 15 0 0 1 0 18 15 15 0 0 1 0-18Z" />
  </Svg>
);

/* ── 操作 ──────────────────────────────────────────────────────────────── */

export const IconSearch = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="11" cy="11" r="7" />
    <path d="m20.5 20.5-4-4" />
  </Svg>
);

export const IconPlus = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 5v14M5 12h14" />
  </Svg>
);

export const IconClose = (p: IconProps) => (
  <Svg {...p}>
    <path d="M18 6 6 18M6 6l12 12" />
  </Svg>
);

export const IconChevronDown = (p: IconProps) => (
  <Svg {...p}>
    <path d="m6 9.5 6 6 6-6" />
  </Svg>
);

export const IconChevronRight = (p: IconProps) => (
  <Svg {...p}>
    <path d="m9.5 6 6 6-6 6" />
  </Svg>
);

export const IconChevronLeft = (p: IconProps) => (
  <Svg {...p}>
    <path d="m14.5 6-6 6 6 6" />
  </Svg>
);

export const IconChevronUp = (p: IconProps) => (
  <Svg {...p}>
    <path d="m6 14.5 6-6 6 6" />
  </Svg>
);

export const IconArrowUp = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 19.5V5" />
    <path d="m5.5 11.5 6.5-6.5 6.5 6.5" />
  </Svg>
);

export const IconArrowDown = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 4.5V19" />
    <path d="m5.5 12.5 6.5 6.5 6.5-6.5" />
  </Svg>
);

export const IconArrowLeft = (p: IconProps) => (
  <Svg {...p}>
    <path d="M19.5 12H5" />
    <path d="m11.5 18.5-6.5-6.5 6.5-6.5" />
  </Svg>
);

export const IconRefresh = (p: IconProps) => (
  <Svg {...p}>
    <path d="M20.5 12a8.5 8.5 0 1 1-2.9-6.36" />
    <path d="M20.8 3.8v5.2h-5.2" />
  </Svg>
);

/** 重启（与「刷新」方向相反，避免同一屏出现两个一样的图标）。 */
export const IconRestart = (p: IconProps) => (
  <Svg {...p}>
    <path d="M3.5 12a8.5 8.5 0 1 0 2.9-6.36" />
    <path d="M3.2 3.8v5.2h5.2" />
  </Svg>
);

export const IconUpload = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 15.5V3.5" />
    <path d="m7 8.5 5-5 5 5" />
    <path d="M4 15.5v3.5a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3.5" />
  </Svg>
);

export const IconDownload = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 3.5v12" />
    <path d="m7 10.5 5 5 5-5" />
    <path d="M4 15.5v3.5a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3.5" />
  </Svg>
);

export const IconTrash = (p: IconProps) => (
  <Svg {...p}>
    <path d="M4 6.5h16" />
    <path d="M9.5 6.5v-2a1 1 0 0 1 1-1h3a1 1 0 0 1 1 1v2" />
    <path d="m6.5 6.5 1 12.2a2 2 0 0 0 2 1.8h5a2 2 0 0 0 2-1.8l1-12.2" />
    <path d="M10.5 10.5v6M13.5 10.5v6" />
  </Svg>
);

export const IconPlay = (p: IconProps) => (
  <Svg {...p}>
    <path d="M8 5.5 19 12 8 18.5z" />
  </Svg>
);

export const IconStop = (p: IconProps) => (
  <Svg {...p}>
    <rect x="6" y="6" width="12" height="12" rx="2.2" />
  </Svg>
);

export const IconSave = (p: IconProps) => (
  <Svg {...p}>
    <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2Z" />
    <path d="M17 21v-7.5H7V21" />
    <path d="M7 3v4.5h8" />
  </Svg>
);

export const IconCopy = (p: IconProps) => (
  <Svg {...p}>
    <rect x="9" y="9" width="12" height="12" rx="2.4" />
    <path d="M5.5 15H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v.5" />
  </Svg>
);

export const IconLocate = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="6.5" />
    <path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3" />
  </Svg>
);

export const IconFilter = (p: IconProps) => (
  <Svg {...p}>
    <path d="M4 5h16l-6.2 7.3v6.2L10.2 20.5v-8.2z" />
  </Svg>
);

export const IconList = (p: IconProps) => (
  <Svg {...p}>
    <path d="M8.5 6h12M8.5 12h12M8.5 18h12" />
    <path d="M3.6 6h.01M3.6 12h.01M3.6 18h.01" />
  </Svg>
);

/** 数据表：带表头的网格。 */
export const IconTable = (p: IconProps) => (
  <Svg {...p}>
    <rect x="2.5" y="4" width="19" height="16" rx="2.4" />
    <path d="M2.5 9.5h19" />
    <path d="M9 9.5V20M15 9.5V20" />
  </Svg>
);

export const IconEdit = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 20h9" />
    <path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4z" />
  </Svg>
);

export const IconExternal = (p: IconProps) => (
  <Svg {...p}>
    <path d="M14 4h6v6" />
    <path d="M20 4 11.5 12.5" />
    <path d="M19 14.5V19a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h4.5" />
  </Svg>
);

/* ── 状态 / AI ─────────────────────────────────────────────────────────── */

/** AI / 助手。 */
export const IconSparkles = (p: IconProps) => (
  <Svg {...p}>
    <path d="M10.5 3 12.2 8.3 17.5 10 12.2 11.7 10.5 17 8.8 11.7 3.5 10 8.8 8.3z" />
    <path d="m18 14.5.9 2.6 2.6.9-2.6.9-.9 2.6-.9-2.6-2.6-.9 2.6-.9z" />
  </Svg>
);

export const IconBot = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3.5" y="8" width="17" height="12.5" rx="3.5" />
    <path d="M12 4.5V8" />
    <circle cx="9" cy="14" r="1.3" />
    <circle cx="15" cy="14" r="1.3" />
  </Svg>
);

export const IconShield = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 21.5s7.5-3.6 7.5-9.5V5.6L12 2.5 4.5 5.6v6.4c0 5.9 7.5 9.5 7.5 9.5Z" />
  </Svg>
);

export const IconShieldCheck = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 21.5s7.5-3.6 7.5-9.5V5.6L12 2.5 4.5 5.6v6.4c0 5.9 7.5 9.5 7.5 9.5Z" />
    <path d="m9 11.8 2.2 2.2 4-4.2" />
  </Svg>
);

export const IconSettings = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="3.2" />
    <path d="M19.2 14.6a1.6 1.6 0 0 0 .32 1.77l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.6 1.6 0 0 0-2.72 1.13V21a2 2 0 0 1-4 0v-.1a1.6 1.6 0 0 0-2.72-1.13l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.6 1.6 0 0 0 3.3 14.2H3.2a2 2 0 0 1 0-4h.1a1.6 1.6 0 0 0 1.13-2.72l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.6 1.6 0 0 0 10 3.5V3.4a2 2 0 0 1 4 0v.1a1.6 1.6 0 0 0 2.72 1.13l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.6 1.6 0 0 0 1.13 2.72h.1a2 2 0 0 1 0 4h-.1a1.6 1.6 0 0 0-1.48 1.42Z" />
  </Svg>
);

export const IconClock = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="8.5" />
    <path d="M12 7.2v5l3.2 1.9" />
  </Svg>
);

export const IconHistory = (p: IconProps) => (
  <Svg {...p}>
    <path d="M3.5 12a8.5 8.5 0 1 0 2.6-6.1" />
    <path d="M3.5 4.5V10h5.5" />
    <path d="M12 8v4.5l3 1.8" />
  </Svg>
);

export const IconCheck = (p: IconProps) => (
  <Svg {...p}>
    <path d="m5 12.5 4.6 4.6L19 7.5" />
  </Svg>
);

export const IconCheckCircle = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="8.7" />
    <path d="m8.4 12.3 2.5 2.5 4.7-5" />
  </Svg>
);

export const IconXCircle = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="8.7" />
    <path d="m9.2 9.2 5.6 5.6M14.8 9.2l-5.6 5.6" />
  </Svg>
);

export const IconAlert = (p: IconProps) => (
  <Svg {...p}>
    <path d="M10.3 3.9 2.5 17.4A2 2 0 0 0 4.2 20.4h15.6a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z" />
    <path d="M12 9.2v4.4" />
    <path d="M12 17h.01" />
  </Svg>
);

export const IconInfo = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="8.7" />
    <path d="M12 11v5.2" />
    <path d="M12 7.8h.01" />
  </Svg>
);

/** 加载态；外层配 `animate-spin`。 */
export const IconLoader = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 3v3.6M12 17.4V21M5.5 5.5l2.6 2.6M15.9 15.9l2.6 2.6M3 12h3.6M17.4 12H21M5.5 18.5l2.6-2.6M15.9 8.1l2.6-2.6" />
  </Svg>
);

/** 端口转发：插头。 */
export const IconPlug = (p: IconProps) => (
  <Svg {...p}>
    <path d="M9 3v6M15 3v6" />
    <path d="M6 9h12v2.8a6 6 0 0 1-12 0z" />
    <path d="M12 17.8V21" />
  </Svg>
);

export const IconKey = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="8" cy="15.5" r="4" />
    <path d="m10.9 12.6 8-8" />
    <path d="m16.6 5.1 2.3 2.3" />
    <path d="m14.2 7.5 2.3 2.3" />
  </Svg>
);

export const IconLock = (p: IconProps) => (
  <Svg {...p}>
    <rect x="4" y="10" width="16" height="11" rx="2.6" />
    <path d="M8 10V7a4 4 0 0 1 8 0v3" />
  </Svg>
);

export const IconUnlock = (p: IconProps) => (
  <Svg {...p}>
    <rect x="4" y="10" width="16" height="11" rx="2.6" />
    <path d="M8 10V7a4 4 0 0 1 7.6-1.8" />
  </Svg>
);

export const IconEye = (p: IconProps) => (
  <Svg {...p}>
    <path d="M2.5 12S6 5.8 12 5.8 21.5 12 21.5 12 18 18.2 12 18.2 2.5 12 2.5 12Z" />
    <circle cx="12" cy="12" r="3" />
  </Svg>
);

export const IconEyeOff = (p: IconProps) => (
  <Svg {...p}>
    <path d="M4 4 20 20" />
    <path d="M9.9 5.9A9.7 9.7 0 0 1 12 5.8c6 0 9.5 6.2 9.5 6.2a17.5 17.5 0 0 1-3.2 3.9" />
    <path d="M6.4 7.9A17.4 17.4 0 0 0 2.5 12s3.5 6.2 9.5 6.2c1.2 0 2.3-.2 3.3-.6" />
    <path d="M10 10a3 3 0 0 0 4 4" />
  </Svg>
);

export const IconCommand = (p: IconProps) => (
  <Svg {...p}>
    <path d="M15 6v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3V6a3 3 0 1 0-3 3h12a3 3 0 1 0-3-3" />
  </Svg>
);

/** 接管：游戏手柄。 */
export const IconGamepad = (p: IconProps) => (
  <Svg {...p}>
    <path d="M7.5 7.5h9a5 5 0 0 1 4.8 6.4l-.7 2.4a2.3 2.3 0 0 1-3.9 1L15 15.5H9l-1.7 1.8a2.3 2.3 0 0 1-3.9-1l-.7-2.4A5 5 0 0 1 7.5 7.5Z" />
    <path d="M8.5 10.5v2.5M7.2 11.7h2.6" />
    <path d="M15.5 11h.01M17.3 12.4h.01" />
  </Svg>
);

export const IconPanelLeft = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3" y="4" width="18" height="16" rx="2.4" />
    <path d="M9.5 4v16" />
  </Svg>
);

export const IconPanelRight = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3" y="4" width="18" height="16" rx="2.4" />
    <path d="M14.5 4v16" />
  </Svg>
);

export const IconDrive = (p: IconProps) => (
  <Svg {...p}>
    <path d="M3 12.5h18" />
    <path d="M5.5 12.5V7.2a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v5.3" />
    <rect x="3" y="12.5" width="18" height="6.8" rx="2.2" />
    <path d="M7 15.9h.01M11 15.9h.01" />
  </Svg>
);

export const IconNetwork = (p: IconProps) => (
  <Svg {...p}>
    <rect x="9" y="2.5" width="6" height="5" rx="1.6" />
    <rect x="2.5" y="16.5" width="6" height="5" rx="1.6" />
    <rect x="15.5" y="16.5" width="6" height="5" rx="1.6" />
    <path d="M12 7.5v4M5.5 16.5v-2.5h13v2.5" />
  </Svg>
);

export const IconActivity = (p: IconProps) => (
  <Svg {...p}>
    <path d="M3 12.5h3.6l2.6-6.4 3.6 12 2.6-5.6H21" />
  </Svg>
);

export const IconCpu = (p: IconProps) => (
  <Svg {...p}>
    <rect x="5" y="5" width="14" height="14" rx="2.4" />
    <rect x="9.2" y="9.2" width="5.6" height="5.6" rx="1.2" />
    <path d="M9 2.5v2.5M15 2.5v2.5M9 19v2.5M15 19v2.5M2.5 9H5M2.5 15H5M19 9h2.5M19 15h2.5" />
  </Svg>
);

export const IconZap = (p: IconProps) => (
  <Svg {...p}>
    <path d="M13.2 2.5 4.8 13.4h6.4l-1 8.1 8.4-10.9h-6.4z" />
  </Svg>
);

/* ── 映射表 ────────────────────────────────────────────────────────────── */

/** 资产类型 → 图标。集中一处，避免各面板各写一套。 */
export const KIND_ICON_MAP = {
  ssh: IconServer,
  winrm: IconGrid,
  local: IconMonitor,
  docker: IconBox,
  mysql: IconDatabase,
  redis: IconLayers,
} as const;

export type AssetKind = keyof typeof KIND_ICON_MAP;

/** 取资产类型对应图标；未知类型回退到服务器图标。 */
export function assetIcon(kind: string) {
  return KIND_ICON_MAP[kind as AssetKind] ?? IconServer;
}
