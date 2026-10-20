export function formatDockerBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "kB", "MB", "GB", "TB", "PB"];
  let size = value;
  let unit = 0;
  while (size >= 1000 && unit < units.length - 1) {
    size /= 1000;
    unit++;
  }
  return `${size >= 100 || unit === 0 ? size.toFixed(0) : size.toFixed(size >= 10 ? 1 : 2)} ${units[unit]}`;
}
