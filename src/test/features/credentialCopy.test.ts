import { describe, expect, it, vi } from "vitest";
import type { RevealedCredential } from "../../ipc/commands";
import { resolveCredentialCopy } from "../../features/credentials/credentialCopy";

function plain(overrides: Partial<RevealedCredential> = {}): RevealedCredential {
  return {
    kind: "private_key",
    value: "PRIVATE KEY BODY",
    source: "inline",
    refPath: null,
    passphrase: "key passphrase",
    ...overrides,
  };
}

describe("resolveCredentialCopy", () => {
  it("copies a hidden inline private-key passphrase, never the key body", async () => {
    const reveal = vi.fn(async () => plain());
    await expect(
      resolveCredentialCopy({ source: "inline", refPath: null }, "passphrase", null, reveal),
    ).resolves.toBe("key passphrase");
    expect(reveal).toHaveBeenCalledOnce();
  });

  it("uses an existing reveal and preserves an explicitly empty passphrase", async () => {
    const reveal = vi.fn(async () => plain());
    await expect(
      resolveCredentialCopy(
        { source: "inline", refPath: null },
        "passphrase",
        plain({ passphrase: "" }),
        reveal,
      ),
    ).resolves.toBe("");
    expect(reveal).not.toHaveBeenCalled();
  });

  it("copies a hidden file-reference private-key passphrase", async () => {
    const reveal = vi.fn(async () => plain({ value: "", source: "file", refPath: "/keys/id" }));
    await expect(
      resolveCredentialCopy({ source: "file", refPath: "/keys/id" }, "passphrase", null, reveal),
    ).resolves.toBe("key passphrase");
  });

  it("copies inline and ordinary password values", async () => {
    const reveal = vi.fn(async () => plain({ kind: "password", value: "password value", source: null }));
    await expect(
      resolveCredentialCopy({ source: null, refPath: null }, "value", null, reveal),
    ).resolves.toBe("password value");
    await expect(
      resolveCredentialCopy({ source: "inline", refPath: null }, "value", plain(), reveal),
    ).resolves.toBe("PRIVATE KEY BODY");
  });

  it("copies a reference path without revealing the credential", async () => {
    const reveal = vi.fn(async () => plain());
    await expect(
      resolveCredentialCopy({ source: "file", refPath: "/keys/id" }, "value", null, reveal),
    ).resolves.toBe("/keys/id");
    expect(reveal).not.toHaveBeenCalled();
  });
});
