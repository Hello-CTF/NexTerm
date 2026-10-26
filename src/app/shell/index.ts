// Shell mode and input-modality queries for code outside src/app/platform.ts and src/app/shell.
export {
  isMobileShell,
  isTouchShell,
  isWidthOverlayViewport,
  readShellMode,
  type ShellMode,
} from "./shellMode";
export { useShell, useShellLayout, type ShellLayout } from "./useShell";
// Touch-friendly controls key on the pointer, not on the shell mode.
export { useCoarsePointer } from "../platform";
