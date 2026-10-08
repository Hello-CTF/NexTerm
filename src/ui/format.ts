export function formatTime(ms: number): string {
  if (!ms) return "从未";
  return new Date(ms).toLocaleString();
}
