import { describe, expect, it, vi } from "vitest";
import {
  findPathLinksInRows,
  findPathsInLine,
  resolveTerminalPath,
  type PathDetectOptions,
} from "../../features/terminal/terminalPathLinks";
import { openTerminalPath, type PathOpenDeps, type PathTarget } from "../../features/terminal/terminalPathOpen";
import type { TextRow } from "../../features/terminal/terminalLinkProvider";

const POSIX: PathDetectOptions = { windows: false, relative: true };
const WINDOWS: PathDetectOptions = { windows: true, relative: true };

const found = (line: string, options = POSIX) =>
  findPathsInLine(line, options).map((m) => ({ path: m.path, line: m.line, col: m.col, text: m.text }));

const one = (path: string, line?: number, col?: number, text = path + (line ? `:${line}` : "") + (col ? `:${col}` : "")) => ({
  path,
  line,
  col,
  text,
});

describe("path detection", () => {
  it.each([
    ["main.go:12", [one("main.go", 12)]],
    ["./src/a.ts:12:5", [one("./src/a.ts", 12, 5)]],
    ["/etc/nginx/nginx.conf", [one("/etc/nginx/nginx.conf")]],
    ["src/a.ts:12:5: error: missing semicolon", [one("src/a.ts", 12, 5)]],
    ["internal/x/y.go:30:foo := bar", [one("internal/x/y.go", 30)]],
    ["  --> src/main.rs:7:9", [one("src/main.rs", 7, 9)]],
    ["src/app.ts(12,5): error TS2304", [{ path: "src/app.ts", line: 12, col: 5, text: "src/app.ts(12,5)" }]],
    ["see /etc/hosts.", [one("/etc/hosts")]],
    ["cat ~/notes/todo.md", [one("~/notes/todo.md")]],
    ["/var", [one("/var")]],
    ["config/.env", [one("config/.env")]],
    ["错误：./src/模块/说明.md:3 未找到", [one("./src/模块/说明.md", 3)]],
    ["编辑 /etc/hosts 后重启", [one("/etc/hosts")]],
    ['  File "/usr/lib/python3/app.py", line 12, in main', [{ path: "/usr/lib/python3/app.py", line: 12, col: undefined, text: "/usr/lib/python3/app.py" }]],
    ['  File "my app/run.py", line 3', [{ path: "my app/run.py", line: 3, col: undefined, text: "my app/run.py" }]],
    ["+++ b/src/x.ts", [one("src/x.ts")]],
  ])("finds %j", (line, expected) => {
    expect(found(line)).toEqual(expected);
  });

  it.each([
    "and/or",
    "TCP/IP",
    "2024/01/02",
    "1/2 done",
    "10 req/s",
    "main.go",
    "version 1.2.3:4",
    "example.com:8080",
    "10.0.0.1:22",
    "user@host:/var/log/syslog",
    "git@github.com:org/repo.git",
    "//cdn.example.com/a.js",
    'File "<stdin>", line 1',
    "12:30:45",
    "/s",
    "打开/etc/hosts失败",
    "C:\\Windows\\System32",
  ])("ignores %j", (line) => {
    expect(found(line)).toEqual([]);
  });

  it("skips relative paths while the working directory is unknown", () => {
    const options = { windows: false, relative: false };
    expect(found("src/a.ts:3 ./b.ts /etc/hosts ~/x.md", options).map((m) => m.path)).toEqual(["/etc/hosts", "~/x.md"]);
  });

  it("finds Windows paths only in Windows sessions, in MSBuild and colon formats", () => {
    expect(found("C:\\src\\App\\Program.cs(12,5): error CS1002", WINDOWS)).toEqual([
      { path: "C:\\src\\App\\Program.cs", line: 12, col: 5, text: "C:\\src\\App\\Program.cs(12,5)" },
    ]);
    expect(found("at D:/work/app.js:4", WINDOWS)).toEqual([one("D:/work/app.js", 4)]);
    expect(found("src\\Lib\\Util.cs(8): warning", WINDOWS)).toEqual([
      { path: "src\\Lib\\Util.cs", line: 8, col: undefined, text: "src\\Lib\\Util.cs(8)" },
    ]);
    expect(found("abc:\\x\\y.cs", WINDOWS)).toEqual([]);
  });
});

describe("path links in terminal rows", () => {
  const rows = (...texts: Array<string | [string, boolean]>): TextRow[] =>
    texts.map((entry) => (typeof entry === "string" ? { text: entry, isWrapped: false } : { text: entry[0], isWrapped: entry[1] }));

  it("does not detect the path inside a URL a second time", () => {
    const links = findPathLinksInRows(rows("open https://example.com/docs/a.html and ./docs/a.html"), 80, POSIX);
    expect(links.map((l) => l.text)).toEqual(["./docs/a.html"]);
  });

  it("joins a path soft-wrapped over two rows", () => {
    const [link] = findPathLinksInRows(rows("error in /home/dev/pro", ["ject/src/main.go:42", true]), 22, POSIX);
    expect(link.text).toBe("/home/dev/project/src/main.go:42");
    expect(link.segments.map((s) => s.row)).toEqual([0, 1]);
    expect(link.segments[1]).toMatchObject({ start: 0, end: "ject/src/main.go:42".length });
  });

  it("places segments by text index on CJK rows", () => {
    const [link] = findPathLinksInRows(rows("文件 ./a.ts:1"), 80, POSIX);
    expect(link.segments).toEqual([{ row: 0, start: 3, end: 11, group: 0 }]);
  });
});

describe("path resolution", () => {
  it("resolves relative paths against the working directory and normalizes dots", () => {
    expect(resolveTerminalPath("./src/a.ts", { cwd: "/home/u/proj", windows: false })).toBe("/home/u/proj/src/a.ts");
    expect(resolveTerminalPath("../lib/b.go", { cwd: "/home/u/proj/cmd", windows: false })).toBe("/home/u/proj/lib/b.go");
    expect(resolveTerminalPath("/etc//nginx/./nginx.conf", { cwd: null, windows: false })).toBe("/etc/nginx/nginx.conf");
    expect(resolveTerminalPath("src/a.ts", { cwd: null, windows: false })).toBeNull();
  });

  it("expands ~ only with a known home directory", () => {
    expect(resolveTerminalPath("~/x.md", { cwd: "/tmp", home: "/home/u", windows: false })).toBe("/home/u/x.md");
    expect(resolveTerminalPath("~/x.md", { cwd: "/tmp", windows: false })).toBeNull();
  });

  it("resolves Windows paths against an OSC 7 drive directory", () => {
    expect(resolveTerminalPath("src\\App.cs", { cwd: "/C:/work/app", windows: true })).toBe("C:/work/app/src/App.cs");
    expect(resolveTerminalPath("D:\\x\\..\\y.cs", { cwd: "/C:/work", windows: true })).toBe("D:/y.cs");
  });
});

describe("opening a path", () => {
  const deps = (overrides: Partial<PathOpenDeps> = {}): PathOpenDeps & { notify: ReturnType<typeof vi.fn>; openFile: ReturnType<typeof vi.fn>; listDirectory: ReturnType<typeof vi.fn> } =>
    ({
      sessionId: "s1",
      sessionHost: () => "web-1",
      homePath: async () => "/home/u",
      listDirectory: vi.fn(async (_id: string, dir: string) =>
        dir === "/home/u/proj/src" ? [{ name: "a.ts", path: "/home/u/proj/src/a.ts", kind: "file" }, { name: "lib", path: "/home/u/proj/src/lib", kind: "dir" }] : [],
      ),
      isEditableFile: (name: string) => name.endsWith(".ts"),
      openFile: vi.fn(),
      openDirectory: vi.fn(),
      notify: vi.fn(),
      describeError: String,
      ...overrides,
    }) as never;
  const target = (path: string, extra: Partial<PathTarget> = {}): PathTarget => ({
    path,
    cwd: "/home/u/proj",
    cwdHost: "web-1",
    windows: false,
    ...extra,
  });

  it("opens an existing file at its line", async () => {
    const d = deps();
    await openTerminalPath(target("src/a.ts", { line: 12, col: 5 }), d);
    expect(d.openFile).toHaveBeenCalledWith("s1", "/home/u/proj/src/a.ts", { line: 12, col: 5 });
    expect(d.listDirectory).toHaveBeenCalledTimes(1);
  });

  it("says when the file does not exist", async () => {
    const d = deps();
    await openTerminalPath(target("src/missing.ts", { line: 3 }), d);
    expect(d.openFile).not.toHaveBeenCalled();
    expect(d.notify).toHaveBeenCalledWith("error", expect.stringContaining("文件不存在"));
    expect(d.notify.mock.calls[0][1]).toContain("/home/u/proj/src/missing.ts");
  });

  it("refuses when the shell's directory is on another host", async () => {
    const d = deps();
    await openTerminalPath(target("src/a.ts", { cwdHost: "db-2" }), d);
    expect(d.listDirectory).not.toHaveBeenCalled();
    expect(d.notify).toHaveBeenCalledWith("info", expect.stringContaining("db-2"));
  });

  it("matches Windows names without case", async () => {
    const d = deps({
      listDirectory: vi.fn(async () => [{ name: "App.cs", path: "C:/work/App.cs", kind: "file" }]),
      isEditableFile: () => true,
    });
    await openTerminalPath(target("app.CS", { cwd: "C:/work", cwdHost: null, windows: true, line: 2 }), d);
    expect(d.openFile).toHaveBeenCalledWith("s1", "C:/work/App.cs", { line: 2 });
  });
});
