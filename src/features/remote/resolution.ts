import { validateResolution } from "../explorer/assetKindFields";

// Reads "1600x900", "1600 x 900", "1600*900" or "1600×900". Returns the size, or the reason it is not usable.
export function parseResolutionInput(text: string): { ok: true; width: number; height: number } | { ok: false; error: string } {
  const match = /^\s*(\d+)\s*[x×*X]\s*(\d+)\s*$/.exec(text);
  if (!match) return { ok: false, error: "请输入“宽 x 高”，例如 1600 x 900" };
  const width = Number(match[1]);
  const height = Number(match[2]);
  const problem = validateResolution(width, height);
  return problem ? { ok: false, error: problem } : { ok: true, width, height };
}

// A window-sized desktop must be even in both directions: the Display Control channel rejects odd sizes.
export function followSize(viewportWidth: number, viewportHeight: number): { width: number; height: number } {
  return {
    width: Math.max(640, Math.floor(viewportWidth) & ~1),
    height: Math.max(480, Math.floor(viewportHeight) & ~1),
  };
}
