import type { CredentialSource, RevealedCredential } from "../../ipc/commands";

export type CredentialCopyField = "value" | "passphrase";

export interface CredentialCopySource {
  source: CredentialSource | null;
  refPath: string | null;
}

export async function resolveCredentialCopy(
  credential: CredentialCopySource,
  field: CredentialCopyField,
  revealed: RevealedCredential | null,
  reveal: () => Promise<RevealedCredential>,
): Promise<string> {
  if (field === "value" && credential.source === "file") return credential.refPath ?? "";
  const plain = revealed ?? (await reveal());
  return field === "passphrase" ? (plain.passphrase ?? "") : plain.value;
}
