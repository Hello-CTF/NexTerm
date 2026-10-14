import { describe, expect, it } from "vitest";
import { formatSshCommand, sshCommandCopyable } from "../../features/explorer/sshCommand";

describe("formatSshCommand", () => {
  it("joins user@host and omits -p for default port 22", () => {
    expect(formatSshCommand({ host: "10.0.0.8", port: 22, username: "root" })).toBe(
      "ssh root@10.0.0.8",
    );
  });

  it("appends -p port for non-default ports", () => {
    expect(formatSshCommand({ host: "edge.example.com", port: 2222, username: "deploy" })).toBe(
      "ssh deploy@edge.example.com -p 2222",
    );
  });

  it("omits segments for missing usernames or ports", () => {
    expect(formatSshCommand({ host: "example.com", port: null, username: null })).toBe(
      "ssh example.com",
    );
    expect(formatSshCommand({ host: "example.com", port: 2222, username: "" })).toBe(
      "ssh example.com -p 2222",
    );
  });

  it("trims fields before joining them", () => {
    expect(formatSshCommand({ host: " 10.0.0.8 ", port: 22, username: " root " })).toBe(
      "ssh root@10.0.0.8",
    );
  });
});

describe("sshCommandCopyable", () => {
  it("allows copying SSH and Docker commands only when a host is present", () => {
    expect(sshCommandCopyable({ kind: "ssh", host: "10.0.0.8" })).toBe(true);
    expect(sshCommandCopyable({ kind: "docker", host: "10.0.0.8" })).toBe(true);
  });

  it("does not offer copying without a host or for non-SSH transports", () => {
    expect(sshCommandCopyable({ kind: "ssh", host: null })).toBe(false);
    expect(sshCommandCopyable({ kind: "ssh", host: "   " })).toBe(false);
    expect(sshCommandCopyable({ kind: "winrm", host: "10.0.0.8" })).toBe(false);
    expect(sshCommandCopyable({ kind: "mysql", host: "10.0.0.8" })).toBe(false);
    expect(sshCommandCopyable({ kind: "local", host: null })).toBe(false);
  });
});
