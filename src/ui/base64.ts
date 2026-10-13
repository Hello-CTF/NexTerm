// Encode in chunks to avoid blowing the call stack when String.fromCharCode spreads large inputs.
const BASE64_CHUNK_BYTES = 32_766;

export function bytesToBase64(bytes: Uint8Array): string {
  const chunks: string[] = [];
  for (let i = 0; i < bytes.length; i += BASE64_CHUNK_BYTES) {
    const chunk = bytes.subarray(i, i + BASE64_CHUNK_BYTES);
    chunks.push(btoa(String.fromCharCode(...chunk)));
  }
  return chunks.join("");
}
