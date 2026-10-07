import { spawn } from "node:child_process";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

export function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
  });
}

function portBusy(port) {
  return new Promise((resolve) => {
    const socket = net.connect({ host: "127.0.0.1", port });
    socket.once("connect", () => {
      socket.destroy();
      resolve(true);
    });
    socket.once("error", () => resolve(false));
  });
}

export async function waitHttp(url, child, timeout = 30_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (child && (child.exitCode !== null || child.signalCode !== null)) {
      throw new Error(`${url}: process exited ${child.exitCode ?? child.signalCode}`);
    }
    try {
      const response = await fetch(url);
      if (response.ok) return response;
    } catch {}
    await sleep(100);
  }
  throw new Error(`timed out waiting for ${url}`);
}

export function stopProcess(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  child.kill("SIGTERM");
}

const owned = new Set();

function killOwned() {
  for (const child of owned) stopProcess(child);
  owned.clear();
}

let hooksInstalled = false;

function installExitHooks() {
  if (hooksInstalled) return;
  hooksInstalled = true;
  process.once("exit", killOwned);
  for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
    process.once(signal, () => {
      killOwned();
      process.kill(process.pid, signal);
    });
  }
}

export async function startVite(options = {}) {
  const root = options.root ?? REPO_ROOT;
  const viteBin = options.viteBin ?? path.join(root, "node_modules", "vite", "bin", "vite.js");
  const port = options.port || Number(process.env.NEXTERM_VITE_PORT || 0) || (await freePort());
  if (await portBusy(port)) {
    throw new Error(`vite port ${port} is already in use; refusing to adopt an existing server`);
  }
  installExitHooks();
  // 直启 vite.js (不经 pnpm 包装): SIGTERM 必须落在真正的 dev server 进程上,
  // 否则包装进程死后 vite 子进程残留占用端口。
  const child = spawn(
    process.execPath,
    [viteBin, "--host", "127.0.0.1", "--port", String(port), "--strictPort"],
    {
      cwd: root,
      env: { ...process.env, NODE_OPTIONS: "" },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  owned.add(child);
  let stderr = "";
  child.stderr.on("data", (chunk) => {
    stderr = `${stderr}${chunk}`.slice(-4096);
  });
  child.stdout.resume();
  let exited = false;
  child.once("exit", () => {
    exited = true;
  });
  let stopPromise = null;
  const handle = {
    process: child,
    port,
    origin: `http://127.0.0.1:${port}`,
    stop() {
      if (stopPromise) return stopPromise;
      owned.delete(child);
      stopPromise = (async () => {
        if (exited) return;
        stopProcess(child);
        const deadline = Date.now() + 5000;
        while (!exited && Date.now() < deadline) await sleep(50);
        if (!exited) {
          child.kill("SIGKILL");
          const killDeadline = Date.now() + 5000;
          while (!exited && Date.now() < killDeadline) await sleep(50);
        }
      })();
      return stopPromise;
    },
  };
  try {
    await waitHttp(handle.origin, child, options.readyTimeout ?? 60_000);
  } catch (error) {
    await handle.stop();
    throw new Error(`${error.message}; vite stderr=${stderr.slice(-1000) || "(empty)"}`);
  }
  return handle;
}
