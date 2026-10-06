/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";
import { Terminal } from "@xterm/xterm";
import { CommandBlockManager, type CommandBlock } from "../../features/terminal/commandBlocks";
import {
  createOscSegmentWriter,
  createOscStreamFilter,
  type OscStreamEvent,
} from "../../features/terminal/oscStream";

const enc = new TextEncoder();

function setup() {
  const term = new Terminal({ cols: 80, rows: 10, allowProposedApi: true });
  const host = document.createElement("div");
  document.body.appendChild(host);
  term.open(host);
  const updates: CommandBlock[][] = [];
  const manager = new CommandBlockManager(term, (blocks) => updates.push(blocks));
  const filter = createOscStreamFilter();
  const write = createOscSegmentWriter(term, (event: OscStreamEvent) => {
    if (event.kind === "command") manager.feedOSC133(event.phase, event.exitCode);
  });
  const latest = () => updates[updates.length - 1] ?? [];
  return { term, manager, filter, write, latest };
}

const drainWrites = () => new Promise((resolve) => setTimeout(resolve, 80));

describe("OSC 133 write ordering", () => {
  it("applies echo/C/output/D from a single chunk in stream order", async () => {
    const h = setup();
    h.write(
      h.filter.push(
        enc.encode("$ make test\r\n\x1b]133;C\x07output line\r\n\x1b]133;D;2\x07$ "),
      ),
    );
    await drainWrites();
    const blocks = h.latest();
    expect(blocks).toHaveLength(1);
    const block = blocks[0];
    expect(block.command).toBe("$ make test");
    expect(block.exitCode).toBe(2);
    expect(block.endedAt).not.toBeNull();
    // The marker lands on the echoed command line, not the pre-echo buffer:
    // C ran after the echo text was parsed.
    expect(block.startLine).toBe(0);
    // D ran after the output text was parsed, so the block covers it.
    const text = h.manager.getBlockText(block.index);
    expect(text).toContain("output line");
    h.term.dispose();
  });

  it("keeps order across chunk boundaries (echo, then C/output/D)", async () => {
    const h = setup();
    h.write(h.filter.push(enc.encode("$ systemctl status\r\n")));
    await drainWrites();
    h.write(h.filter.push(enc.encode("\x1b]133;C\x07active (running)\r\n\x1b]133;D;0\x07$ ")));
    await drainWrites();
    const blocks = h.latest();
    expect(blocks).toHaveLength(1);
    expect(blocks[0].command).toBe("$ systemctl status");
    expect(blocks[0].exitCode).toBe(0);
    expect(h.manager.getBlockText(blocks[0].index)).toContain("active (running)");
    h.term.dispose();
  });

  it("does not fire events before the preceding text is parsed", async () => {
    const h = setup();
    const seen: string[] = [];
    const filter = createOscStreamFilter();
    const write = createOscSegmentWriter(
      h.term,
      (event) => {
        if (event.kind !== "command") return;
        const line = h.term.buffer.active.getLine(0)?.translateToString(true) ?? "";
        seen.push(`${event.phase}:${line.trim()}`);
      },
    );
    write(filter.push(enc.encode("first\r\n\x1b]133;D;1\x07")));
    await drainWrites();
    // D must observe the already-parsed "first" line, not an empty buffer.
    expect(seen).toEqual(["D:first"]);
    h.term.dispose();
  });
});
