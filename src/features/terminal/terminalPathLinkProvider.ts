import type { ILink, ILinkProvider, Terminal } from "@xterm/xterm";
import { cellMap, cellRange, softRuns, windowBounds, type TextRow } from "./terminalLinkProvider";
import { findPathLinksInRows, type PathDetectOptions, type PathLink } from "./terminalPathLinks";

export interface PathLinkProviderHandlers {
  /** Detection settings for the current moment, or null while path links are off for this terminal. */
  options: () => PathDetectOptions | null;
  /** Whether the link at this buffer row (0-based, where it starts) can be offered. */
  accept?(link: PathLink, row: number): boolean;
  activate(event: MouseEvent, link: PathLink, row: number): void;
  hover(event: MouseEvent, link: PathLink, row: number): void;
  leave(event: MouseEvent, link: PathLink, row: number): void;
}

/**
 * Path links for xterm. Register it before the URL provider: xterm prefers the
 * earlier provider where ranges meet, and path detection already skips URLs,
 * so a path wins only over the URL provider's host:port targets.
 */
export function createTerminalPathLinkProvider(term: Terminal, handlers: PathLinkProviderHandlers): ILinkProvider {
  return {
    provideLinks(y, callback) {
      const options = handlers.options();
      const row0 = y - 1;
      const buf = term.buffer.active;
      if (!options || !buf.getLine(row0)) {
        callback(undefined);
        return;
      }
      const { first, last } = windowBounds(term, row0);
      const rows: TextRow[] = [];
      for (let r = first; r <= last; r++) {
        const line = buf.getLine(r);
        rows.push({ text: line?.translateToString(true) ?? "", isWrapped: r > first && line?.isWrapped === true });
      }
      const idx = row0 - first;
      const maps = new Map<number, ReturnType<typeof cellMap>>();
      const mapOfRow = (row: number) => {
        let map = maps.get(row);
        if (!map) {
          map = cellMap(buf.getLine(first + row)!);
          maps.set(row, map);
        }
        return map;
      };
      const result: ILink[] = [];
      for (const link of findPathLinksInRows(rows, term.cols, options)) {
        const linkRow = first + link.segments[0].row;
        if (handlers.accept && !handlers.accept(link, linkRow)) continue;
        for (const run of softRuns(link.segments)) {
          if (!run.some((seg) => seg.row === idx)) continue;
          const head = run[0];
          const tail = run[run.length - 1];
          result.push({
            range: {
              start: cellRange(mapOfRow(head.row), head, first + head.row + 1).start,
              end: cellRange(mapOfRow(tail.row), tail, first + tail.row + 1).end,
            },
            text: link.text,
            decorations: { underline: true, pointerCursor: true },
            activate: (event) => handlers.activate(event, link, linkRow),
            hover: (event) => handlers.hover(event, link, linkRow),
            leave: (event) => handlers.leave(event, link, linkRow),
          });
        }
      }
      callback(result.length > 0 ? result : undefined);
    },
  };
}
