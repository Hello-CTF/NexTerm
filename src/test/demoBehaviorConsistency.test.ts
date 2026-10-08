import { beforeEach, describe, expect, it, vi } from "vitest";
import { mockInvoke } from "../demo/mock";
import { DemoShell } from "../demo/shell";
import { fsFileContent, fsTree } from "../demo/data";
import { createConversationStream } from "../features/ai/conversationStream";
import type { ChatItem } from "../features/ai/conversation";

beforeEach(() => {
  vi.stubGlobal("window", globalThis);
});

interface CollectedEvents {
  events: Record<string, unknown>[];
  channel: { onmessage: (msg: unknown) => void };
}

function collectEvents(): CollectedEvents {
  const events: Record<string, unknown>[] = [];
  return {
    events,
    channel: {
      onmessage: (msg: unknown) => {
        if (msg instanceof Uint8Array) return;
        events.push(msg as Record<string, unknown>);
      },
    },
  };
}

async function waitFor(events: Record<string, unknown>[], type: string, timeoutMs = 8000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const hit = events.find((e) => e.type === type);
    if (hit) return hit;
    await new Promise((r) => window.setTimeout(r, 50));
  }
  throw new Error(`event ${type} not received within ${timeoutMs}ms`);
}

describe("demo fs_checksum 返回真实确定性校验值", () => {
  it("sha256 与 md5 都对文件内容算出真实哈希", async () => {
    const path = "/home/deploy/checksum-vector.txt";
    fsFileContent[path] = "abc";
    await expect(mockInvoke("fs_checksum", { args: { path, algo: "sha256" } })).resolves.toBe(
      "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
    );
    await expect(mockInvoke("fs_checksum", { args: { path, algo: "md5" } })).resolves.toBe(
      "900150983cd24fb0d6963f7d28e17f72",
    );
    delete fsFileContent[path];
  });

  it("同一文件重复计算结果一致，写入新内容后校验值跟着变", async () => {
    const path = "/home/deploy/checksum-deterministic.txt";
    fsFileContent[path] = "v1";
    const first = await mockInvoke("fs_checksum", { args: { path, algo: "md5" } });
    const second = await mockInvoke("fs_checksum", { args: { path, algo: "md5" } });
    expect(second).toBe(first);
    fsFileContent[path] = "v2";
    await expect(mockInvoke("fs_checksum", { args: { path, algo: "md5" } })).resolves.not.toBe(first);
    delete fsFileContent[path];
  });

  it("拒绝白名单之外的算法", async () => {
    await expect(
      mockInvoke("fs_checksum", { args: { path: "/etc/nginx/nginx.conf", algo: "sha512" } }),
    ).rejects.toMatchObject({ code: "bad_param" });
  });
});

describe("demo redis_command 对未内置命令如实说明未执行", () => {
  it("未知命令返回未执行说明（全角括号），内置命令不受影响", async () => {
    const result = (await mockInvoke("redis_command", { args: { args: ["SET", "k", "v"] } })) as string;
    expect(result).toContain("没有执行");
    expect(result).toContain("（演示模式");
    expect(result).not.toContain("已执行");
    await expect(mockInvoke("redis_command", { args: { args: ["PING"] } })).resolves.toBe("PONG");
  });
});

describe("demo AI 授权写入真实落到演示文件系统", () => {
  it("confirm 不含编造的请求计数；授权后文件更新且生成 .nexterm-bak 备份", async () => {
    const path = "/etc/nginx/nginx.conf";
    const backup = `${path}.nexterm-bak`;
    const original = fsFileContent[path];
    const treeBefore = [...(fsTree["/etc/nginx"] ?? [])];
    const { events, channel } = collectEvents();
    try {
      const started = (await mockInvoke("ai_chat", {
        conversationId: "conv-write-consistency",
        message: "把 nginx 配置改一下",
        channel,
      })) as { jobId: string };
      const confirm = await waitFor(events, "confirmRequired");
      expect(confirm.reason).toBe("文件系统写操作");
      expect(String(confirm.rendered)).not.toContain("第 1 次");
      expect(String(confirm.rendered)).not.toContain("请求写权限");
      const preview = confirm.preview as { before: string; after: string };
      for (const line of preview.before.split("\n")) expect(original).toContain(line);

      await mockInvoke("ai_confirm", { jobId: started.jobId, callId: confirm.id, decision: "allow" });
      const done = await waitFor(events, "done");

      expect(fsFileContent[path]).toContain("worker_processes auto;");
      expect(fsFileContent[path]).toContain("server_tokens off;");
      expect(fsFileContent[path]).toContain("client_max_body_size 64m");
      expect(fsFileContent[path]).not.toContain("worker_processes 1;");
      for (const line of preview.after.split("\n")) expect(fsFileContent[path]).toContain(line);
      expect(fsFileContent[backup]).toBe(original);
      expect(fsTree["/etc/nginx"]?.some((e) => e.path === backup)).toBe(true);
      expect(String(done.answer)).toContain("三处配置");
      expect(String(done.answer)).toContain(".nexterm-bak");
    } finally {
      fsFileContent[path] = original;
      delete fsFileContent[backup];
      fsTree["/etc/nginx"] = treeBefore;
    }
  }, 15000);

  it("第二次授权修改同一文件：预览与结果基于当前内容，已达标时明确无需变更", async () => {
    const path = "/etc/nginx/nginx.conf";
    const backup = `${path}.nexterm-bak`;
    const original = fsFileContent[path];
    const treeBefore = [...(fsTree["/etc/nginx"] ?? [])];
    try {
      const first = collectEvents();
      const started = (await mockInvoke("ai_chat", {
        conversationId: "conv-write-twice",
        message: "把 nginx 配置改一下",
        channel: first.channel,
      })) as { jobId: string };
      const confirm = await waitFor(first.events, "confirmRequired");
      await mockInvoke("ai_confirm", { jobId: started.jobId, callId: confirm.id, decision: "allow" });
      await waitFor(first.events, "done");
      const afterFirst = fsFileContent[path];
      expect(afterFirst).toContain("worker_processes auto;");

      const second = collectEvents();
      await mockInvoke("ai_chat", {
        conversationId: "conv-write-twice",
        message: "把 nginx 配置改一下",
        channel: second.channel,
      });
      const done = await waitFor(second.events, "done");
      expect(second.events.some((e) => e.type === "confirmRequired")).toBe(false);
      expect(second.events.some((e) => e.type === "fileChange")).toBe(false);
      expect(String(done.answer)).toContain("无需变更");
      expect(String(done.answer)).not.toContain("三处配置");
      expect(fsFileContent[path]).toBe(afterFirst);
      expect(fsFileContent[backup]).toBe(original);
    } finally {
      fsFileContent[path] = original;
      delete fsFileContent[backup];
      fsTree["/etc/nginx"] = treeBefore;
    }
  }, 20000);

  it("client_max_body_size 值不符（32m）时生成真实修复计划而非误报无需变更", async () => {
    const path = "/etc/nginx/nginx.conf";
    const backup = `${path}.nexterm-bak`;
    const original = fsFileContent[path];
    const treeBefore = [...(fsTree["/etc/nginx"] ?? [])];
    const cfg = [
      "user  nginx;",
      "worker_processes auto;",
      "events {",
      "    worker_connections  1024;",
      "}",
      "http {",
      "    keepalive_timeout  65;",
      "    server_tokens off;",
      "    client_max_body_size 32m;",
      "}",
    ].join("\n");
    fsFileContent[path] = cfg;
    const { events, channel } = collectEvents();
    try {
      const started = (await mockInvoke("ai_chat", {
        conversationId: "conv-write-32m",
        message: "把 nginx 配置改一下",
        channel,
      })) as { jobId: string };
      const confirm = await waitFor(events, "confirmRequired");
      const preview = confirm.preview as { before: string; after: string };
      expect(preview.before).toContain("client_max_body_size 32m;");
      expect(preview.after).toContain("client_max_body_size 64m;");
      await mockInvoke("ai_confirm", { jobId: started.jobId, callId: confirm.id, decision: "allow" });
      const done = await waitFor(events, "done");
      expect(fsFileContent[path]).toContain("client_max_body_size 64m;");
      expect(fsFileContent[path]).not.toContain("32m");
      expect(String(done.answer)).toContain("一处配置");
      expect(String(done.answer)).toContain("64m");
      expect(String(done.answer)).not.toContain("无需变更");
    } finally {
      fsFileContent[path] = original;
      delete fsFileContent[backup];
      fsTree["/etc/nginx"] = treeBefore;
    }
  }, 15000);

  it("缺 server_tokens 锚点时退到 keepalive 锚点生成真实修复计划", async () => {
    const path = "/etc/nginx/nginx.conf";
    const backup = `${path}.nexterm-bak`;
    const original = fsFileContent[path];
    const treeBefore = [...(fsTree["/etc/nginx"] ?? [])];
    fsFileContent[path] = original.replace(/\n[ \t]*server_tokens [^;]+;/, "");
    const { events, channel } = collectEvents();
    try {
      const started = (await mockInvoke("ai_chat", {
        conversationId: "conv-write-noanchor",
        message: "把 nginx 配置改一下",
        channel,
      })) as { jobId: string };
      const confirm = await waitFor(events, "confirmRequired");
      await mockInvoke("ai_confirm", { jobId: started.jobId, callId: confirm.id, decision: "allow" });
      const done = await waitFor(events, "done");
      expect(fsFileContent[path]).toContain("worker_processes auto;");
      expect(fsFileContent[path]).toContain("client_max_body_size 64m;");
      expect(String(done.answer)).toContain("两处配置");
      expect(String(done.answer)).toContain("新增 `client_max_body_size 64m`");
      expect(String(done.answer)).not.toContain("无需变更");
    } finally {
      fsFileContent[path] = original;
      delete fsFileContent[backup];
      fsTree["/etc/nginx"] = treeBefore;
    }
  }, 15000);

  it("锚点全缺、配置异常时不把空 plan 当无需变更，明确报错且不动文件", async () => {
    const path = "/etc/nginx/nginx.conf";
    const original = fsFileContent[path];
    const broken = original
      .replace("worker_processes 1;", "worker_processes auto;")
      .replace(/\n[ \t]*server_tokens [^;]+;/, "")
      .replace(/\n[ \t]*keepalive_timeout[^;]*;/, "");
    fsFileContent[path] = broken;
    const { events, channel } = collectEvents();
    try {
      await mockInvoke("ai_chat", {
        conversationId: "conv-write-broken",
        message: "把 nginx 配置改一下",
        channel,
      });
      const done = await waitFor(events, "done");
      expect(events.some((e) => e.type === "confirmRequired")).toBe(false);
      expect(events.some((e) => e.type === "fileChange")).toBe(false);
      expect(String(done.answer)).toContain("没能改");
      expect(String(done.answer)).toContain("没有做任何变更");
      expect(String(done.answer)).not.toContain("无需变更");
      expect(fsFileContent[path]).toBe(broken);
    } finally {
      fsFileContent[path] = original;
    }
  }, 15000);

  it("缩进注释掉的 server_tokens 不算有效指令：锚点回退 keepalive 生成真实新增计划", async () => {
    const path = "/etc/nginx/nginx.conf";
    const backup = `${path}.nexterm-bak`;
    const original = fsFileContent[path];
    const treeBefore = [...(fsTree["/etc/nginx"] ?? [])];
    const cfg = [
      "user  nginx;",
      "worker_processes auto;",
      "http {",
      "    keepalive_timeout  65;",
      "    # server_tokens off;",
      "}",
    ].join("\n");
    fsFileContent[path] = cfg;
    const { events, channel } = collectEvents();
    try {
      const started = (await mockInvoke("ai_chat", {
        conversationId: "conv-write-comment-st",
        message: "把 nginx 配置改一下",
        channel,
      })) as { jobId: string };
      const confirm = await waitFor(events, "confirmRequired");
      await mockInvoke("ai_confirm", { jobId: started.jobId, callId: confirm.id, decision: "allow" });
      const done = await waitFor(events, "done");
      expect(fsFileContent[path]).toMatch(/^[ \t]*client_max_body_size 64m;[ \t]*$/m);
      expect(String(done.answer)).toContain("一处配置");
      expect(String(done.answer)).toContain("新增 `client_max_body_size 64m`");
      expect(String(done.answer)).not.toContain("无需变更");
    } finally {
      fsFileContent[path] = original;
      delete fsFileContent[backup];
      fsTree["/etc/nginx"] = treeBefore;
    }
  }, 15000);

  it("缩进注释掉的 client_max_body_size 64m 不算已达标：生成真实新增计划", async () => {
    const path = "/etc/nginx/nginx.conf";
    const backup = `${path}.nexterm-bak`;
    const original = fsFileContent[path];
    const treeBefore = [...(fsTree["/etc/nginx"] ?? [])];
    const cfg = [
      "user  nginx;",
      "worker_processes auto;",
      "http {",
      "    keepalive_timeout  65;",
      "    server_tokens off;",
      "    # client_max_body_size 64m;",
      "}",
    ].join("\n");
    fsFileContent[path] = cfg;
    const { events, channel } = collectEvents();
    try {
      const started = (await mockInvoke("ai_chat", {
        conversationId: "conv-write-comment-cmb",
        message: "把 nginx 配置改一下",
        channel,
      })) as { jobId: string };
      const confirm = await waitFor(events, "confirmRequired");
      await mockInvoke("ai_confirm", { jobId: started.jobId, callId: confirm.id, decision: "allow" });
      const done = await waitFor(events, "done");
      expect(fsFileContent[path]).toContain("# client_max_body_size 64m;");
      expect(fsFileContent[path]).toMatch(/^[ \t]*client_max_body_size 64m;[ \t]*$/m);
      expect(String(done.answer)).toContain("一处配置");
      expect(String(done.answer)).not.toContain("无需变更");
    } finally {
      fsFileContent[path] = original;
      delete fsFileContent[backup];
      fsTree["/etc/nginx"] = treeBefore;
    }
  }, 15000);

  it("server_tokens 只有注释行且其余已达标时明确报错，不答无需变更", async () => {
    const path = "/etc/nginx/nginx.conf";
    const original = fsFileContent[path];
    const cfg = [
      "user  nginx;",
      "worker_processes auto;",
      "http {",
      "    keepalive_timeout  65;",
      "    # server_tokens off;",
      "    client_max_body_size 64m;",
      "}",
    ].join("\n");
    fsFileContent[path] = cfg;
    const { events, channel } = collectEvents();
    try {
      await mockInvoke("ai_chat", {
        conversationId: "conv-write-comment-only",
        message: "把 nginx 配置改一下",
        channel,
      });
      const done = await waitFor(events, "done");
      expect(events.some((e) => e.type === "confirmRequired")).toBe(false);
      expect(events.some((e) => e.type === "fileChange")).toBe(false);
      expect(String(done.answer)).toContain("没能改");
      expect(String(done.answer)).toContain("没有做任何变更");
      expect(String(done.answer)).not.toContain("无需变更");
      expect(fsFileContent[path]).toBe(cfg);
    } finally {
      fsFileContent[path] = original;
    }
  }, 15000);

  it("新建文件的授权写入同样真实创建文件", async () => {
    const path = "/etc/nginx/conf.d/upload.conf";
    const treeBefore = [...(fsTree["/etc/nginx/conf.d"] ?? [])];
    const { events, channel } = collectEvents();
    try {
      const started = (await mockInvoke("ai_chat", {
        conversationId: "conv-create-consistency",
        message: "新建一个上传配置",
        channel,
      })) as { jobId: string };
      const confirm = await waitFor(events, "confirmRequired");
      await mockInvoke("ai_confirm", { jobId: started.jobId, callId: confirm.id, decision: "allow" });
      await waitFor(events, "done");
      expect(fsFileContent[path]).toContain("client_max_body_size 64m");
      expect(fsTree["/etc/nginx/conf.d"]?.some((e) => e.path === path)).toBe(true);
    } finally {
      delete fsFileContent[path];
      fsTree["/etc/nginx/conf.d"] = treeBefore;
    }
  }, 15000);

  it("只读问答不再冒出未授权的文件修改事件", async () => {
    const { events, channel } = collectEvents();
    await mockInvoke("ai_chat", {
      conversationId: "conv-readonly-consistency",
      message: "看看概况",
      channel,
    });
    const done = await waitFor(events, "done");
    expect(events.some((e) => e.type === "fileChange")).toBe(false);
    expect(String(done.answer)).toContain("工具结果");
    expect(String(done.answer)).not.toContain("可回放");
  }, 15000);
});

describe("demo shell 文案与行为一致", () => {
  function runShell(inputs: string[], opts: { user?: string; host?: string } = {}): string {
    let out = "";
    const shell = new DemoShell((text) => {
      out += text;
    }, opts);
    shell.start();
    for (const input of inputs) shell.input(`${input}\r`);
    return out;
  }

  it("提示符与虚拟主机名跟随传入的 user/host", () => {
    const out = runShell(["hostname", "whoami"], { user: "demo", host: "localhost" });
    expect(out).toContain("demo@localhost");
    expect(out).toContain("虚拟的 localhost");
    expect(out).toContain("localhost\n");
    expect(out).toContain("demo\n");
  });

  it("默认身份仍是 deploy@web-01", () => {
    const out = runShell(["hostname"]);
    expect(out).toContain("deploy@web-01");
    expect(out).toContain("web-01\n");
  });

  it("systemctl restart/start/stop 如实说明未模拟、无变更", () => {
    for (const sub of ["restart", "start", "stop"]) {
      const out = runShell([`systemctl ${sub} nginx`]);
      expect(out).toContain(`未模拟 systemctl ${sub}`);
      expect(out).toContain("没有做任何变更");
      expect(out).not.toContain("需要权限");
    }
  });

  it("help 列出的 mkdir 真实可用", () => {
    const out = runShell(["mkdir /data/app/tmpdemo"]);
    expect(out).not.toContain("command not found");
    expect(fsTree["/data/app"]?.some((e) => e.path === "/data/app/tmpdemo")).toBe(true);
    const help = runShell(["help"]);
    expect(help).toContain("mkdir");
    delete fsTree["/data/app/tmpdemo"];
    fsTree["/data/app"] = fsTree["/data/app"].filter((e) => e.path !== "/data/app/tmpdemo");
  });

  it("mkdir 遇到既有文件或既有目录都报 File exists 且文件树不变", () => {
    const before = [...(fsTree["/etc/nginx"] ?? [])];
    const out = runShell(["mkdir /etc/nginx/nginx.conf", "mkdir /etc/nginx"]);
    expect(out.match(/File exists/g)).toHaveLength(2);
    expect(fsTree["/etc/nginx/nginx.conf"]).toBeUndefined();
    expect(fsTree["/etc/nginx"]).toEqual(before);
  });
});

describe("demo 终端 attach 提示与资产一致", () => {
  function textChannel(chunks: Uint8Array[]) {
    return {
      onmessage: (msg: unknown) => {
        if (msg instanceof Uint8Array) chunks.push(msg);
      },
    };
  }

  it("本机资产 attach 后横幅与提示符都是本机，接回文案不再声称回放", async () => {
    const sess = (await mockInvoke("session_connect_local")) as { id: string };
    const chunks: Uint8Array[] = [];
    const tabId = (await mockInvoke("terminal_attach", {
      sessionId: sess.id,
      channel: textChannel(chunks),
      clientId: "consistency-test",
    })) as string;
    await new Promise((r) => window.setTimeout(r, 300));
    const first = new TextDecoder().decode(Uint8Array.from(chunks.flatMap((c) => [...c])));
    expect(first).toContain("已连到 当前设备");
    expect(first).toContain("demo@localhost");
    expect(first).not.toContain("deploy@web-01");

    const chunks2: Uint8Array[] = [];
    await mockInvoke("terminal_attach_tab", {
      tabId,
      channel: textChannel(chunks2),
      clientId: "consistency-test",
    });
    await new Promise((r) => window.setTimeout(r, 250));
    const second = new TextDecoder().decode(Uint8Array.from(chunks2.flatMap((c) => [...c])));
    expect(second).toContain("已接回后台终端");
    expect(second).not.toContain("回放");
    expect(second).toContain("demo@localhost");
  }, 15000);
});

describe("demo takeover 屏幕事件链与 conversation stream 一致", () => {
  it("mock 发 4 屏，stream 只保留最近一屏，结尾文案与真实保留一致", async () => {
    const { events, channel } = collectEvents();
    await mockInvoke("ai_takeover_run", { instruction: "安装 nginx 并启动", channel });
    const done = await waitFor(events, "done", 12_000);
    expect(events.filter((e) => e.type === "screen")).toHaveLength(4);
    expect(String(done.answer)).toContain("最近一屏");

    let frames: (() => void)[] = [];
    const s = createConversationStream((cb) => {
      frames.push(cb);
      return () => {
        frames = frames.filter((f) => f !== cb);
      };
    });
    s.beginRun(1);
    for (const ev of events) s.pushEvent(1, ev);
    const pending = frames;
    frames = [];
    for (const cb of pending) cb();

    const screens = s
      .getState()
      .items.filter((i): i is Extract<ChatItem, { role: "tool" }> => i.role === "tool" && i.name === "read_screen");
    expect(screens).toHaveLength(1);
    expect(screens[0].text).toContain("systemctl is-active");
    expect(screens[0].text).not.toContain("apt-get install");
  }, 20000);
});
