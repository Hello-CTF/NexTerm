import { describe, expect, it } from "vitest";
import { statementAtCursor } from "../../features/db/sqlStatements";
import { parseRedisArgs } from "../../features/db/redisArgs";
import { CommandHistory } from "../../features/db/commandHistory";

// at returns the doc without the "|" marker and the marker's position.
function at(marked: string): [string, number] {
  const pos = marked.indexOf("|");
  return [marked.slice(0, pos) + marked.slice(pos + 1), pos];
}

describe("statementAtCursor", () => {
  it.each([
    ["SELECT |1;\nSELECT 2;", "SELECT 1;"],
    ["SELECT 1;|\nSELECT 2;", "SELECT 1;"],
    ["SELECT 1;\n|SELECT 2;", "SELECT 2;"],
    ["SELECT 1;\nSELECT 2|", "SELECT 2"],
    ["SELECT 1;\nSELECT 2;\n\n|", "SELECT 2;"],
    ["SELECT 1;\n-- trailing note|", "SELECT 1;"],
    ["|\n\nSELECT 1;", "SELECT 1;"],
    ["-- lead\nSELECT 1;|", "-- lead\nSELECT 1;"],
    ["SELECT 'a;b' AS x;| SELECT 2;", "SELECT 'a;b' AS x;"],
    ["SELECT 'it''s; ok', \"x;y\";| SELECT 2;", "SELECT 'it''s; ok', \"x;y\";"],
    ["SELECT 1 /* ; */ + 2;| SELECT 3;", "SELECT 1 /* ; */ + 2;"],
    ["SELECT 1 -- ;\n + 2;| SELECT 3;", "SELECT 1 -- ;\n + 2;"],
  ])("splits %j on top-level semicolons for both dialects", (marked, want) => {
    const [doc, pos] = at(marked);
    expect(statementAtCursor(doc, pos, "mysql")).toBe(want);
    expect(statementAtCursor(doc, pos, "postgres")).toBe(want);
  });

  it.each([
    ["SELECT `a;b` FROM t;| SELECT 2;", "SELECT `a;b` FROM t;"],
    ["SELECT 'a\\';b';| SELECT 2;", "SELECT 'a\\';b';"],
    ["SELECT 1 # ;\n;| SELECT 2;", "SELECT 1 # ;\n;"],
    ["SELECT 1--1;| SELECT 2;", "SELECT 1--1;"],
  ])("handles MySQL quoting in %j", (marked, want) => {
    const [doc, pos] = at(marked);
    expect(statementAtCursor(doc, pos, "mysql")).toBe(want);
  });

  it.each([
    [
      "CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql;| SELECT 2;",
      "CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql;",
    ],
    ["DO $body$ BEGIN PERFORM 1; END $body$;| SELECT 2;", "DO $body$ BEGIN PERFORM 1; END $body$;"],
    ["SELECT 'C:\\';| SELECT 2;", "SELECT 'C:\\';"],
    ["SELECT E'a\\';b';| SELECT 2;", "SELECT E'a\\';b';"],
    ["SELECT 1 /* a /* ; */ ; */;| SELECT 2;", "SELECT 1 /* a /* ; */ ; */;"],
    ["SELECT $1;| SELECT 2;", "SELECT $1;"],
  ])("handles PostgreSQL quoting in %j", (marked, want) => {
    const [doc, pos] = at(marked);
    expect(statementAtCursor(doc, pos, "postgres")).toBe(want);
  });

  it("returns an empty statement for a buffer of comments and whitespace", () => {
    expect(statementAtCursor("  -- nothing\n/* here */ ;", 3, "mysql")).toBe("");
    expect(statementAtCursor("", 0, "postgres")).toBe("");
  });
});

describe("parseRedisArgs", () => {
  it.each([
    ["SET k v", ["SET", "k", "v"]],
    ["  GET   k  ", ["GET", "k"]],
    ['SET k "a b"', ["SET", "k", "a b"]],
    ['SET k "line\\nnext\\ttab \\"q\\" \\\\"', ["SET", "k", 'line\nnext\ttab "q" \\']],
    ['SET k "\\x41\\xe4\\xbd\\xa0"', ["SET", "k", "A你"]],
    ["SET k 'a \\n \"b\" \\'c\\''", ["SET", "k", "a \\n \"b\" 'c'"]],
    ['SET k ""', ["SET", "k", ""]],
    ['SET k foo"bar baz"', ["SET", "k", "foobar baz"]],
    ["SET 键 值", ["SET", "键", "值"]],
    ["", []],
  ])("splits %j like redis-cli", (line, args) => {
    expect(parseRedisArgs(line)).toEqual({ ok: true, args });
  });

  it.each(['SET k "open', "SET k 'open", 'SET k "a"b', "SET k 'a'b"])(
    "rejects unbalanced quotes in %j",
    (line) => {
      expect(parseRedisArgs(line)).toEqual({ ok: false, error: "unbalanced-quotes" });
    },
  );

  it("rejects escaped bytes that are not UTF-8", () => {
    expect(parseRedisArgs('SET k "\\xff"')).toEqual({ ok: false, error: "invalid-utf8" });
  });
});

describe("CommandHistory", () => {
  it("walks entries and restores the unsent draft", () => {
    const history = new CommandHistory();
    history.push("GET a");
    history.push("GET b");
    history.push("GET b");
    expect(history.older("draft")).toBe("GET b");
    expect(history.older("GET b")).toBe("GET a");
    expect(history.older("GET a")).toBeNull();
    expect(history.newer()).toBe("GET b");
    expect(history.newer()).toBe("draft");
    expect(history.newer()).toBeNull();
  });

  it("keeps only the newest entries", () => {
    const history = new CommandHistory(2);
    for (const command of ["a", "b", "c"]) history.push(command);
    expect(history.older("")).toBe("c");
    expect(history.older("c")).toBe("b");
    expect(history.older("b")).toBeNull();
  });
});
