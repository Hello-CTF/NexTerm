/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { Terminal } from "@xterm/xterm";
import { createReplayTerminal } from "../../features/terminal/EarlierHistoryPanel";

const REQUESTS =
  "\x1b[c" + // primary device attributes
  "\x1b[>c" + // secondary device attributes
  "\x1b[6n" + // cursor position report
  "\x1b]52;c;aGVsbG8=\x07" + // clipboard write
  "\x1b]9;done\x07" + // notification
  "\x1b]0;title\x07";

function written(terminal: Terminal, data: string): Promise<void> {
  return new Promise((resolve) => terminal.write(data, resolve));
}

function lines(terminal: Terminal): string[] {
  const buffer = terminal.buffer.active;
  return Array.from({ length: buffer.length }, (_, row) => buffer.getLine(row)?.translateToString(true) ?? "");
}

describe("earlier history replay terminal", () => {
  it("answers no terminal queries that a live terminal answers", async () => {
    const live = new Terminal({ allowProposedApi: true });
    const liveReplies: string[] = [];
    live.onData((data) => liveReplies.push(data));
    await written(live, REQUESTS);
    // The fixture really contains queries a terminal answers.
    expect(liveReplies.length).toBeGreaterThan(0);
    live.dispose();

    const { terminal, dispose } = createReplayTerminal(80, 24);
    const replies: string[] = [];
    terminal.onData((data) => replies.push(data));
    terminal.onBinary((data) => replies.push(data));
    await written(terminal, `before\r\n${REQUESTS}after\r\n`);
    expect(replies).toEqual([]);
    expect(lines(terminal).slice(0, 2)).toEqual(["before", "after"]);
    dispose();
  });

  it("keeps full-screen program output in the scrollable buffer", async () => {
    const { terminal, dispose } = createReplayTerminal(40, 5);
    await written(terminal, "shell line\r\n\x1b[?1049hfull screen\r\n\x1b[?1049lback\r\n");
    expect(terminal.buffer.active.type).toBe("normal");
    expect(lines(terminal).slice(0, 3)).toEqual(["shell line", "full screen", "back"]);
    dispose();
  });
});
