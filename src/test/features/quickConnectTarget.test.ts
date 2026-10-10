import { describe, expect, it } from "vitest";
import {
  formatQuickConnectTarget,
  parseQuickConnectTarget,
} from "../../app/quickConnectTarget";

describe("parseQuickConnectTarget", () => {
  it("解析 user@host:port", () => {
    expect(parseQuickConnectTarget("deploy@example.com:2222")).toEqual({
      host: "example.com",
      port: 2222,
      username: "deploy",
    });
  });

  it("解析 host:port，用户名为空", () => {
    expect(parseQuickConnectTarget("10.0.0.8:22")).toEqual({
      host: "10.0.0.8",
      port: 22,
      username: null,
    });
  });

  it("解析 ssh://user@host:port", () => {
    expect(parseQuickConnectTarget("ssh://root@example.com:22")).toEqual({
      host: "example.com",
      port: 22,
      username: "root",
    });
  });

  it("scheme 大小写不敏感", () => {
    expect(parseQuickConnectTarget("SSH://Example.COM")).toEqual({
      host: "Example.COM",
      port: 22,
      username: null,
    });
  });

  it("user@host 缺省端口 22", () => {
    expect(parseQuickConnectTarget("deploy@example.com")).toEqual({
      host: "example.com",
      port: 22,
      username: "deploy",
    });
  });

  it("ssh://host 视为显式快速连接", () => {
    expect(parseQuickConnectTarget("ssh://example.com")).toEqual({
      host: "example.com",
      port: 22,
      username: null,
    });
  });

  it("裸主机名与裸 IP 不解析，保持资产搜索", () => {
    expect(parseQuickConnectTarget("example.com")).toBeNull();
    expect(parseQuickConnectTarget("10.0.0.8")).toBeNull();
    expect(parseQuickConnectTarget("web-01")).toBeNull();
  });

  it("端口非法时不解析", () => {
    expect(parseQuickConnectTarget("host:abc")).toBeNull();
    expect(parseQuickConnectTarget("host:0")).toBeNull();
    expect(parseQuickConnectTarget("host:99999")).toBeNull();
    expect(parseQuickConnectTarget("host:")).toBeNull();
  });

  it("非 ssh scheme 不解析", () => {
    expect(parseQuickConnectTarget("ftp://example.com")).toBeNull();
    expect(parseQuickConnectTarget("mysql://root@db:3306")).toBeNull();
  });

  it("支持方括号 IPv6", () => {
    expect(parseQuickConnectTarget("root@[::1]:2222")).toEqual({
      host: "::1",
      port: 2222,
      username: "root",
    });
    expect(parseQuickConnectTarget("[::1]:22")).toEqual({
      host: "::1",
      port: 22,
      username: null,
    });
    expect(parseQuickConnectTarget("ssh://[::1]")).toEqual({
      host: "::1",
      port: 22,
      username: null,
    });
  });

  it("裸 IPv6 与残缺输入不解析", () => {
    expect(parseQuickConnectTarget("::1")).toBeNull();
    expect(parseQuickConnectTarget("[::1")).toBeNull();
    expect(parseQuickConnectTarget("@host")).toBeNull();
    expect(parseQuickConnectTarget("user@")).toBeNull();
    expect(parseQuickConnectTarget("ssh://")).toBeNull();
    expect(parseQuickConnectTarget("")).toBeNull();
    expect(parseQuickConnectTarget("   ")).toBeNull();
    expect(parseQuickConnectTarget("user name@host")).toBeNull();
    expect(parseQuickConnectTarget("host/path")).toBeNull();
  });
});

describe("formatQuickConnectTarget", () => {
  it("22 端口省略，其余保留", () => {
    expect(formatQuickConnectTarget({ host: "example.com", port: 22, username: null })).toBe(
      "example.com",
    );
    expect(formatQuickConnectTarget({ host: "example.com", port: 2222, username: "deploy" })).toBe(
      "deploy@example.com:2222",
    );
  });

  it("IPv6 主机统一加方括号并可解析往返", () => {
    const withUsername = { host: "::1", port: 22, username: "root" };
    const withoutUsername = { host: "2001:db8::10", port: 2222, username: null };

    expect(formatQuickConnectTarget(withUsername)).toBe("root@[::1]");
    expect(formatQuickConnectTarget(withoutUsername)).toBe("[2001:db8::10]:2222");
    expect(formatQuickConnectTarget({ host: "::1", port: 22, username: null })).toBe("[::1]");
    expect(parseQuickConnectTarget(formatQuickConnectTarget(withUsername))).toEqual(withUsername);
    expect(parseQuickConnectTarget(formatQuickConnectTarget(withoutUsername))).toEqual(withoutUsername);
  });
});
