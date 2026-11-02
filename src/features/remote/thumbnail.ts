// A small JPEG of a canvas for the session overview. Capped width and quality keep each one to a few kilobytes.
export function drawThumbnail(source: HTMLCanvasElement | null, maxWidth: number): string | null {
  if (!source || source.width < 2 || source.height < 2) return null;
  const width = Math.min(maxWidth, source.width);
  const height = Math.max(1, Math.round((source.height * width) / source.width));
  const target = document.createElement("canvas");
  target.width = width;
  target.height = height;
  const context = target.getContext("2d");
  if (!context) return null;
  try {
    context.drawImage(source, 0, 0, width, height);
    return target.toDataURL("image/jpeg", 0.6);
  } catch {
    return null;
  }
}
