/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";
import { argon2idKey, blake2b } from "../features/auth/argon2";

function hex(data: Uint8Array): string {
  return [...data].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function bytes(text: string): Uint8Array {
  return new TextEncoder().encode(text);
}

// 向量由 Go golang.org/x/crypto/argon2(v0.57.0) IDKey 生成(.scratch/genvec.go 一次性输出)。
const GO_ARGON2ID_VECTORS: {
  password: string;
  salt: string;
  time: number;
  memory: number;
  threads: number;
  key: string;
}[] = [
  {
    password: "password",
    salt: "somesalt12345678",
    time: 3,
    memory: 32,
    threads: 4,
    key: "cd342db86d5658c8e3267ba1c32c14f66bd95c49ca2c842dbff0e298d37817c9",
  },
  {
    password: "correct horse battery staple",
    salt: "0123456789abcdef",
    time: 1,
    memory: 8192,
    threads: 1,
    key: "134d180cae7bf730852877729435966ede036ffdf67eac851183c21a9a855d7c",
  },
  {
    password: "p@ss w0rd 密码",
    salt: "another-salt-16b",
    time: 2,
    memory: 4096,
    threads: 2,
    key: "1476eb21231d63b2c932a8ef4981d361977c34f5e555deb79abf675ab54df21d",
  },
];

describe("argon2id 与 Go x/crypto 互通", () => {
  it("小参数向量一致", () => {
    for (const v of GO_ARGON2ID_VECTORS) {
      const got = argon2idKey(bytes(v.password), bytes(v.salt), v.time, v.memory, v.threads, 32);
      expect(hex(got)).toBe(v.key);
    }
  });

  it("生产参数(t=3, m=64MiB, p=4)向量一致", () => {
    const got = argon2idKey(bytes("password"), bytes("somesalt12345678"), 3, 65536, 4, 32);
    expect(hex(got)).toBe("64d388aeb007b664336c2357dfdd47c496340c12d8ee87dd33f05ae923aa5718");
  }, 60_000);
});

describe("blake2b 基础向量", () => {
  it("空输入 BLAKE2b-512", () => {
    expect(hex(blake2b(new Uint8Array(0), 64))).toBe(
      "786a02f742015903c6c6fd852552d272912f4740e15847618a86e217f71f5419d25e1031afee585313896444934eb04b903a685b1448b755d56f701afe9be2ce",
    );
  });

  it("abc 的 BLAKE2b-512", () => {
    expect(hex(blake2b(bytes("abc"), 64))).toBe(
      "ba80a53f981c4d0d6a2797b69f12f6e94c212f14685ac4b74b12bb6fdbffa2d17d87c5392aab792dc252d5de4533cc9518d38aa8dbf1925ab92386edd4009923",
    );
  });

  it(" BLAKE2b-32(参数化输出长度)", () => {
    expect(hex(blake2b(bytes("abc"), 32))).toBe(
      "bddd813c634239723171ef3fee98579b94964e3bb1cb3e427262c8c068d52319",
    );
  });
});
