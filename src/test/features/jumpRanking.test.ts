import { describe, expect, it } from "vitest";
import { freezeOrder, jumpMatchTier, rankJumpItems, type JumpAttention, type JumpCandidate } from "../../app/jump/jumpRanking";

type Item = JumpCandidate & { id: string };

function item(id: string, section: Item["section"], label: string, extra: Partial<Item> = {}): Item {
  return { id, section, label, ...extra };
}

const ids = (items: Item[]) => items.map((entry) => entry.id);

describe("jumpMatchTier", () => {
  it("prefers name prefix, then name substring, field substring and fuzzy subsequences", () => {
    expect(jumpMatchTier(item("a", "host", "web-01"), "web")).toBe(0);
    expect(jumpMatchTier(item("a", "host", "my-web-01"), "web")).toBe(1);
    expect(jumpMatchTier(item("a", "host", "backend", { keywords: ["web.internal"] }), "web")).toBe(2);
    expect(jumpMatchTier(item("a", "host", "web-prod-01"), "wp1")).toBe(3);
    expect(jumpMatchTier(item("a", "host", "backend", { keywords: ["web.prod"] }), "wbp")).toBe(4);
    expect(jumpMatchTier(item("a", "host", "backend"), "zz")).toBeNull();
  });

  it("requires every token to match and ignores case and surrounding whitespace", () => {
    const candidate = item("a", "host", "Web-01", { keywords: ["10.0.0.8", "root", null] });
    expect(jumpMatchTier(candidate, "  WEB root ")).toBe(2);
    expect(jumpMatchTier(candidate, "web admin")).toBeNull();
  });
});

describe("rankJumpItems", () => {
  const items = [
    item("cmd-split", "command", "向右分屏"),
    item("host-web", "host", "web-01", { recency: 0.4 }),
    item("session-old", "session", "build", { recency: 1 }),
    item("host-db", "host", "db-01", { recency: 0.9 }),
    item("session-new", "session", "deploy", { recency: 5 }),
    item("cmd-settings", "command", "打开设置"),
  ];

  it("groups an empty query into sessions, hosts and commands, newest first and commands in input order", () => {
    expect(ids(rankJumpItems(items, ""))).toEqual([
      "session-new",
      "session-old",
      "host-db",
      "host-web",
      "cmd-split",
      "cmd-settings",
    ]);
  });

  it("orders a query by match quality before section", () => {
    const list = [
      item("session-sub", "session", "my-web"),
      item("host-prefix", "host", "web-01"),
      item("cmd-fuzzy", "command", "w-e-b"),
    ];
    expect(ids(rankJumpItems(list, "web"))).toEqual(["host-prefix", "session-sub", "cmd-fuzzy"]);
  });

  it("ranks attention states ahead of recency when a lookup is given and treats unknown states as other", () => {
    const attention: Record<string, JumpAttention> = { "session-old": "needsMe", "host-web": "justEnded" };
    const ranked = rankJumpItems(items, "", { attentionOf: (entry) => attention[entry.id] });
    expect(ids(ranked)).toEqual([
      "session-old",
      "session-new",
      "host-web",
      "host-db",
      "cmd-split",
      "cmd-settings",
    ]);
  });

  it("falls back to recency when the lookup knows no state", () => {
    expect(ids(rankJumpItems(items, "", { attentionOf: () => undefined }))).toEqual(ids(rankJumpItems(items, "")));
  });

  it("does not mutate its input", () => {
    const before = ids(items);
    rankJumpItems(items, "web");
    expect(ids(items)).toEqual(before);
  });
});

describe("freezeOrder", () => {
  it("keeps shown rows in place, drops removed ones and appends new ones in ranked order", () => {
    expect(freezeOrder(["b", "a", "c"], ["a", "d", "b", "e"])).toEqual(["b", "a", "d", "e"]);
  });

  it("returns the ranked order without a snapshot", () => {
    expect(freezeOrder(null, ["a", "b"])).toEqual(["a", "b"]);
  });
});
