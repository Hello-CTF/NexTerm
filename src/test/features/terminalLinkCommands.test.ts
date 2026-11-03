import { describe, expect, it, vi } from "vitest";
import { terminalLinkCommands } from "../../app/jump/terminalLinkCommands";

describe("terminalLinkCommands", () => {
  it("offers the panel and one command per extracted URL", () => {
    const source = {
      extract: () => [
        { url: "https://a.example.com/x", context: "see https://a.example.com/x", row: 3 },
        { url: "https://b.example.com/y", context: "", row: 1 },
      ],
      open: vi.fn(),
      openPanel: vi.fn(),
    };
    const commands = terminalLinkCommands(source, "Ctrl+Shift+U");
    const panel = commands.find((c) => c.label === "终端链接面板");
    expect(panel?.hint).toBe("Ctrl+Shift+U");
    panel?.run();
    expect(source.openPanel).toHaveBeenCalled();

    const links = commands.filter((c) => c.section === "当前终端链接");
    expect(links).toHaveLength(2);
    expect(links[0].label).toContain("a.example.com");
    expect(links[0].hint).toContain("see");
    links[0].run();
    expect(source.open).toHaveBeenCalledWith("https://a.example.com/x");
  });

  it("hides control and invisible characters in the hint", () => {
    const source = {
      extract: () => [{ url: "https://a.example.com/x", context: "ok\u202e\u200b\u0007 line", row: 1 }],
      open: vi.fn(),
      openPanel: vi.fn(),
    };
    const link = terminalLinkCommands(source).find((c) => c.section === "当前终端链接");
    expect(link?.hint).toBe("ok line");
  });

  it("caps the URL commands at 50", () => {
    const many = Array.from({ length: 80 }, (_, i) => ({ url: `https://h.example.com/${i}`, context: "", row: i }));
    const commands = terminalLinkCommands({ extract: () => many, open: vi.fn(), openPanel: vi.fn() });
    expect(commands.filter((c) => c.section === "当前终端链接")).toHaveLength(50);
  });
});
