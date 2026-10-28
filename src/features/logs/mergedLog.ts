export interface MergedLogLine {
  sequence: number;
  sourceId: string;
  sessionId: string;
  host: string;
  text: string;
  stderr: boolean;
  timeMs: number;
}

export interface LogFilters {
  include?: RegExp;
  exclude?: RegExp;
  highlight?: RegExp;
  error?: string;
}

export function compileLogFilters(include: string, exclude: string): LogFilters {
  try {
    const includeRegex = include ? new RegExp(include, "u") : undefined;
    const excludeRegex = exclude ? new RegExp(exclude, "u") : undefined;
    return {
      include: includeRegex,
      exclude: excludeRegex,
      highlight: include ? new RegExp(include, "gu") : undefined,
    };
  } catch (cause) {
    return { error: cause instanceof Error ? cause.message : String(cause) };
  }
}

export function filterMergedLines(lines: readonly MergedLogLine[], filters: LogFilters): MergedLogLine[] {
  if (filters.error) return [...lines];
  return lines.filter((line) => {
    if (filters.include && !filters.include.test(line.text)) return false;
    if (filters.exclude && filters.exclude.test(line.text)) return false;
    return true;
  });
}

export interface HighlightSegment {
  text: string;
  matched: boolean;
}

export function highlightSegments(text: string, regex?: RegExp): HighlightSegment[] {
  if (!regex) return [{ text, matched: false }];
  regex.lastIndex = 0;
  const segments: HighlightSegment[] = [];
  let offset = 0;
  for (;;) {
    const match = regex.exec(text);
    if (!match) break;
    const start = match.index;
    const end = start + match[0].length;
    if (end > start) {
      if (start > offset) segments.push({ text: text.slice(offset, start), matched: false });
      segments.push({ text: text.slice(start, end), matched: true });
      offset = end;
    }
    if (end === start) regex.lastIndex = start + 1;
  }
  if (offset < text.length || segments.length === 0) {
    segments.push({ text: text.slice(offset), matched: false });
  }
  return segments;
}

export class MergedLogBuffer {
  private lines: MergedLogLine[] = [];
  private pausedSnapshot: MergedLogLine[] | null = null;
  private dropped = 0;

  constructor(private readonly capacity: number) {
    if (!Number.isInteger(capacity) || capacity < 1) throw new Error("日志行数上限必须是正整数");
  }

  append(line: MergedLogLine): void {
    if (!Number.isSafeInteger(line.sequence) || line.sequence <= 0) return;
    let low = 0;
    let high = this.lines.length;
    while (low < high) {
      const middle = (low + high) >>> 1;
      const sequence = this.lines[middle].sequence;
      if (sequence === line.sequence) return;
      if (sequence < line.sequence) low = middle + 1;
      else high = middle;
    }
    this.lines.splice(low, 0, line);
    if (this.lines.length > this.capacity) {
      const count = this.lines.length - this.capacity;
      this.lines.splice(0, count);
      this.dropped += count;
    }
  }

  setPaused(paused: boolean): void {
    if (paused && this.pausedSnapshot === null) this.pausedSnapshot = [...this.lines];
    if (!paused) this.pausedSnapshot = null;
  }

  clear(): void {
    this.lines = [];
    this.pausedSnapshot = this.pausedSnapshot === null ? null : [];
    this.dropped = 0;
  }

  visibleLines(): readonly MergedLogLine[] {
    return this.pausedSnapshot ?? this.lines;
  }

  retainedLines(): readonly MergedLogLine[] {
    return this.lines;
  }

  get droppedCount(): number {
    return this.dropped;
  }
}

// Theme-aware host colors (--nx-host-N in tokens.css). Amber and rose are
// excluded so host labels never look like warnings or stderr.
const HOST_COLORS = [
  "text-(--nx-host-1)",
  "text-(--nx-host-2)",
  "text-(--nx-host-3)",
  "text-(--nx-host-4)",
  "text-(--nx-host-5)",
  "text-(--nx-host-6)",
] as const;

export const HOST_COLOR_COUNT = HOST_COLORS.length;

// Colors are handed out by order of first appearance so distinct hosts only
// share a color once more hosts than palette entries are in play.
const hostColorIndex = new Map<string, number>();

export function hostColor(sessionId: string): string {
  let index = hostColorIndex.get(sessionId);
  if (index === undefined) {
    index = hostColorIndex.size % HOST_COLORS.length;
    hostColorIndex.set(sessionId, index);
  }
  return HOST_COLORS[index];
}

export function resetHostColors(): void {
  hostColorIndex.clear();
}

export function formatExportedLine(line: MergedLogLine): string {
  return `[${line.host}] ${line.text}`;
}

export function exportMergedLines(lines: readonly MergedLogLine[]): string {
  return lines.map(formatExportedLine).join("\n") + (lines.length > 0 ? "\n" : "");
}
