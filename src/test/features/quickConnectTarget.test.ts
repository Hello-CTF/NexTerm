import { describe, expect, it } from "vitest";
import {
  formatQuickConnectTarget,
  parseQuickConnectTarget,
} from "../../app/quickConnectTarget";

describe("parseQuickConnectTarget", () => {
  it("parses user@host:port", () => {
    expect(parseQuickConnectTarget("deploy@example.com:2222")).toEqual({
      host: "example.com",
      port: 2222,
      username: "deploy",
    });
  });

  it("parses host:port with no username", () => {
    expect(parseQuickConnectTarget("10.0.0.8:22")).toEqual({
      host: "10.0.0.8",
      port: 22,
      username: null,
    });
  });

  it("parses ssh://user@host:port", () => {
    expect(parseQuickConnectTarget("ssh://root@example.com:22")).toEqual({
      host: "example.com",
      port: 22,
      username: "root",
    });
  });

  it("matches schemes case-insensitively", () => {
    expect(parseQuickConnectTarget("SSH://Example.COM")).toEqual({
      host: "Example.COM",
      port: 22,
      username: null,
    });
  });

  it("defaults user@host to port 22", () => {
    expect(parseQuickConnectTarget("deploy@example.com")).toEqual({
      host: "example.com",
      port: 22,
      username: "deploy",
    });
  });

  it("treats ssh://host as an explicit quick connection", () => {
    expect(parseQuickConnectTarget("ssh://example.com")).toEqual({
      host: "example.com",
      port: 22,
      username: null,
    });
  });

  it("does not parse bare hostnames or IP addresses, preserving asset search", () => {
    expect(parseQuickConnectTarget("example.com")).toBeNull();
    expect(parseQuickConnectTarget("10.0.0.8")).toBeNull();
    expect(parseQuickConnectTarget("web-01")).toBeNull();
  });

  it("does not parse invalid ports", () => {
    expect(parseQuickConnectTarget("host:abc")).toBeNull();
    expect(parseQuickConnectTarget("host:0")).toBeNull();
    expect(parseQuickConnectTarget("host:99999")).toBeNull();
    expect(parseQuickConnectTarget("host:")).toBeNull();
  });

  it("does not parse non-SSH schemes", () => {
    expect(parseQuickConnectTarget("ftp://example.com")).toBeNull();
    expect(parseQuickConnectTarget("mysql://root@db:3306")).toBeNull();
  });

  it("supports bracketed IPv6 addresses", () => {
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

  it("does not parse bare IPv6 addresses or incomplete input", () => {
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
  it("omits port 22 and preserves other ports", () => {
    expect(formatQuickConnectTarget({ host: "example.com", port: 22, username: null })).toBe(
      "example.com",
    );
    expect(formatQuickConnectTarget({ host: "example.com", port: 2222, username: "deploy" })).toBe(
      "deploy@example.com:2222",
    );
  });

  it("brackets IPv6 hosts and round-trips through the parser", () => {
    const withUsername = { host: "::1", port: 22, username: "root" };
    const withoutUsername = { host: "2001:db8::10", port: 2222, username: null };

    expect(formatQuickConnectTarget(withUsername)).toBe("root@[::1]");
    expect(formatQuickConnectTarget(withoutUsername)).toBe("[2001:db8::10]:2222");
    expect(formatQuickConnectTarget({ host: "::1", port: 22, username: null })).toBe("[::1]");
    expect(parseQuickConnectTarget(formatQuickConnectTarget(withUsername))).toEqual(withUsername);
    expect(parseQuickConnectTarget(formatQuickConnectTarget(withoutUsername))).toEqual(withoutUsername);
  });
});
