import { describe, expect, it } from "vitest";
import { classifyFailure } from "../../features/remote/connectFlow";
import { describeRdpFailure, describeVncFailure, parseNstatus, stripBuildPaths } from "../../features/remote/rdpFailure";
import { engineFailureOf, redactSecrets } from "../../features/remote/RdpClient";
import { parseResolutionInput, followSize } from "../../features/remote/resolution";
import type { EngineFailure } from "../../features/remote/remoteTypes";

const CI_PATH = "/home/runner/work/IronRDP/IronRDP/crates/ironrdp-async/src/connector.rs:107";
const rdp = (patch: Partial<EngineFailure>): EngineFailure => ({ protocol: "rdp", stage: "connect", message: "x", ...patch });
const credssp = (code: string): EngineFailure => {
  const text = `[CredSSP @ ${CI_PATH}] CredSSP: InvalidToken: CredSSP server returned an error status; status is NSTATUS code ${code}`;
  return rdp({ message: stripBuildPaths(text.split("\n")[0]), detail: text, rdpKind: 0 });
};

describe("describeRdpFailure", () => {
  it.each([
    ["0xC000006D", "用户名或密码不正确"],
    ["0xc000006a", "密码不正确"],
    ["0xC0000064", "用户名不存在"],
    ["0xC0000071", "密码已过期"],
    ["0xC0000224", "需要先修改密码"],
    ["0xC0000072", "账户已被禁用"],
    ["0xC0000234", "账户已被锁定"],
    ["0xC000006E", "账户受到限制"],
    ["0xC000015B", "没有远程登录权限"],
    ["0xC0000193", "账户已过期"],
  ])("explains the logon status %s", (code, phrase) => {
    const text = describeRdpFailure(credssp(code));
    expect(text.title).toContain("登录被拒绝");
    expect(text.title).toContain(phrase);
    expect(text.detail).toContain("NSTATUS");
  });

  it("maps 0xC00700EA to the generic login-rejected message without naming a cause", () => {
    const text = describeRdpFailure(credssp("0xc00700ea"));
    expect(text.title).toBe("登录被拒绝");
    expect(text.advice).toContain("用户名");
    expect(text.advice).toContain("域");
    expect(text.advice).not.toMatch(/锁定|过期|禁用/);
  });

  it("maps an unknown status and a CredSSP error without a status to the generic message", () => {
    expect(describeRdpFailure(credssp("0xdeadbeef")).title).toBe("登录被拒绝");
    expect(describeRdpFailure(rdp({ message: "CredSSP: something", detail: "CredSSP: something", rdpKind: 0 })).title).toBe("登录被拒绝");
  });

  it("never puts a CI path or raw engine text in the headline, but keeps it in the details", () => {
    for (const code of ["0xC000006D", "0xc00700ea"]) {
      const text = describeRdpFailure(credssp(code));
      expect(`${text.title}${text.advice}`).not.toMatch(/runner|\.rs|CredSSP:|NSTATUS/);
      expect(text.detail).toContain("/home/runner/work/IronRDP");
    }
  });

  it("explains each IronRDP error kind", () => {
    expect(describeRdpFailure(rdp({ rdpKind: 1 })).title).toContain("登录被拒绝");
    expect(describeRdpFailure(rdp({ rdpKind: 2 })).title).toContain("登录被拒绝");
    expect(describeRdpFailure(rdp({ rdpKind: 3 })).title).toContain("拒绝了访问");
    expect(describeRdpFailure(rdp({ rdpKind: 5 })).title).toContain("中继");
    expect(describeRdpFailure(rdp({ rdpKind: 6 })).advice).toContain("NLA");
  });

  it("reads RDCleanPath status, TLS alert and relay Winsock codes", () => {
    expect(describeRdpFailure(rdp({ rdpKind: 4, httpStatus: 403 })).title).toContain("证书");
    expect(describeRdpFailure(rdp({ rdpKind: 4, httpStatus: 401 })).title).toContain("票据");
    expect(describeRdpFailure(rdp({ rdpKind: 4, tlsAlert: 48 })).title).toContain("TLS");
    expect(describeRdpFailure(rdp({ rdpKind: 4, wsaError: 10061 })).title).toContain("拒绝了连接");
    expect(describeRdpFailure(rdp({ rdpKind: 4, wsaError: 10060 })).title).toContain("超时");
    expect(describeRdpFailure(rdp({ rdpKind: 4, wsaError: 11001 })).title).toContain("找不到");
    expect(describeRdpFailure(rdp({ rdpKind: 4, wsaError: 10050 })).title).toContain("无法连接");
    expect(describeRdpFailure(rdp({ rdpKind: 4 })).title).toContain("握手");
  });

  it("maps the connector state error honestly, with a retry hint and the raw text", () => {
    const raw = "[invalid state (this is a bug) @ crates/ironrdp-connector/src/lib.rs:405] general error";
    const text = describeRdpFailure(rdp({ message: stripBuildPaths(raw), detail: raw, rdpKind: 0 }));
    expect(text.title).toBe("RDP 连接在握手阶段失败");
    expect(text.advice).toContain("重新连接");
    expect(text.advice).not.toMatch(/bug|general error/);
    expect(text.detail).toContain("lib.rs:405");
  });

  it("falls back to a generic message for unknown English text and keeps Chinese app messages", () => {
    const unknown = describeRdpFailure(rdp({ message: "weird engine thing" }));
    expect(unknown.title).toBe("RDP 连接失败");
    expect(unknown.advice).not.toContain("weird");
    expect(unknown.detail).toContain("weird engine thing");
    const known = describeRdpFailure(rdp({ message: "远程桌面组件加载超时" }));
    expect(known.advice).toBe("远程桌面组件加载超时");
    expect(describeRdpFailure(rdp({ message: "", wasConnected: true })).title).toContain("中断");
  });
});

describe("helpers", () => {
  it("parses NSTATUS codes in any case and pads them", () => {
    expect(parseNstatus("status is NSTATUS code 0xc000006d")).toBe("C000006D");
    expect(parseNstatus("NSTATUS code 0x5")).toBe("00000005");
    expect(parseNstatus("nothing")).toBeNull();
  });

  it("strips absolute and relative source locations", () => {
    expect(stripBuildPaths(`[CredSSP @ ${CI_PATH}] CredSSP: bad`)).toBe("[CredSSP] CredSSP: bad");
    expect(stripBuildPaths("[x @ crates/a/src/lib.rs:405] general error")).toBe("[x] general error");
    expect(stripBuildPaths("plain text")).toBe("plain text");
  });

  it("describes VNC failures without raw English headlines", () => {
    expect(describeVncFailure({ protocol: "vnc", stage: "security", message: "Authentication failed" }).title).toBe("VNC 安全协商失败");
    const chinese = describeVncFailure({ protocol: "vnc", stage: "connect", message: "无法连接到主机" });
    expect(chinese.advice).toBe("无法连接到主机");
  });
});

describe("classifyFailure with readable text", () => {
  it("treats every CredSSP failure as a login rejection that is never retried", () => {
    const failure = classifyFailure(credssp("0xc00700ea"));
    expect(failure.kind).toBe("auth");
    expect(failure.title).toBe("登录被拒绝");
    expect(failure.detail).toContain("NSTATUS");
    expect(failure.message).not.toMatch(/runner|\.rs/);
  });

  it("carries title and details for connector and relay failures", () => {
    const failure = classifyFailure(rdp({ rdpKind: 4, wsaError: 10061 }));
    expect(failure).toMatchObject({ kind: "fatal", title: "远程主机拒绝了连接" });
    expect(classifyFailure(rdp({ rdpKind: 4, wsaError: 10061, wasConnected: true })).kind).toBe("retryable");
  });
});

describe("resolution input", () => {
  it("accepts the usual separators and rejects bad sizes", () => {
    expect(parseResolutionInput("1600 x 900")).toEqual({ ok: true, width: 1600, height: 900 });
    expect(parseResolutionInput("1366×768")).toEqual({ ok: true, width: 1366, height: 768 });
    expect(parseResolutionInput("100x100")).toMatchObject({ ok: false });
    expect(parseResolutionInput("9000x9000")).toMatchObject({ ok: false });
    expect(parseResolutionInput("big")).toMatchObject({ ok: false });
  });

  it("rounds a window-sized desktop down to even numbers", () => {
    expect(followSize(1281, 721)).toEqual({ width: 1280, height: 720 });
    expect(followSize(100, 100)).toEqual({ width: 640, height: 480 });
  });
});

describe("secret redaction", () => {
  it("removes the password and ticket from detail and message", () => {
    const error = {
      kind: () => 0,
      rdcleanpathDetails: () => undefined,
      backtrace: () => "auth with hunter2 ticket tkt-123 failed\nframe hunter2",
    };
    const failure = engineFailureOf(error, ["hunter2", "tkt-123", ""]);
    expect(failure.detail).toBe("auth with *** ticket *** failed\nframe ***");
    expect(failure.message).not.toMatch(/hunter2|tkt-123/);
    expect(JSON.stringify(failure)).not.toMatch(/hunter2|tkt-123/);
    expect(redactSecrets("keep", [""])).toBe("keep");
  });
});
