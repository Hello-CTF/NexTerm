import { describe, expect, it } from "vitest";
import { formatSshCommand, sshCommandCopyable } from "../../features/explorer/sshCommand";

describe("formatSshCommand", () => {
  it("拼接 user@host, 默认端口 22 省略 -p", () => {
    expect(formatSshCommand({ host: "10.0.0.8", port: 22, username: "root" })).toBe(
      "ssh root@10.0.0.8",
    );
  });

  it("非默认端口追加 -p port", () => {
    expect(formatSshCommand({ host: "edge.example.com", port: 2222, username: "deploy" })).toBe(
      "ssh deploy@edge.example.com -p 2222",
    );
  });

  it("用户名或端口缺省时省略对应片段", () => {
    expect(formatSshCommand({ host: "example.com", port: null, username: null })).toBe(
      "ssh example.com",
    );
    expect(formatSshCommand({ host: "example.com", port: 2222, username: "" })).toBe(
      "ssh example.com -p 2222",
    );
  });

  it("字段先裁剪再拼接", () => {
    expect(formatSshCommand({ host: " 10.0.0.8 ", port: 22, username: " root " })).toBe(
      "ssh root@10.0.0.8",
    );
  });
});

describe("sshCommandCopyable", () => {
  it("ssh / docker 且有主机才可复制", () => {
    expect(sshCommandCopyable({ kind: "ssh", host: "10.0.0.8" })).toBe(true);
    expect(sshCommandCopyable({ kind: "docker", host: "10.0.0.8" })).toBe(true);
  });

  it("无主机或非 SSH 传输不出现", () => {
    expect(sshCommandCopyable({ kind: "ssh", host: null })).toBe(false);
    expect(sshCommandCopyable({ kind: "ssh", host: "   " })).toBe(false);
    expect(sshCommandCopyable({ kind: "winrm", host: "10.0.0.8" })).toBe(false);
    expect(sshCommandCopyable({ kind: "mysql", host: "10.0.0.8" })).toBe(false);
    expect(sshCommandCopyable({ kind: "local", host: null })).toBe(false);
  });
});
