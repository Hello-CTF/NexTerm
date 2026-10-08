
import { containers, fsFileContent, fsTree, redisKeys } from "./data";

const DIM = "\x1b[2m";
const RESET = "\x1b[0m";
const RED = "\x1b[31m";
const GREEN = "\x1b[32m";
const YELLOW = "\x1b[33m";
const BLUE = "\x1b[34m";
const BOLD = "\x1b[1m";

const HOME_DIR = "/home/deploy";

function clock(offsetMs = 0): string {
  const d = new Date(Date.now() - offsetMs);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

export class DemoShell {
  private cwd: string;
  private buf = "";
  private history: string[] = [];
  private histIdx = -1;
  private readonly user: string;
  private readonly host: string;

  constructor(
    private readonly write: (text: string) => void,
    opts: { cwd?: string; user?: string; host?: string } = {},
  ) {
    this.cwd = opts.cwd ?? HOME_DIR;
    this.user = opts.user ?? "deploy";
    this.host = opts.host ?? "web-01";
  }

  start() {
    this.out(`${DIM}Last login: ${new Date(Date.now() - 46 * 60_000).toDateString()} ${clock(46 * 60_000)} from 10.0.0.8${RESET}`);
    this.out(`${DIM}# 演示模式：这是一台虚拟的 ${this.host}，可以随便敲。输入 help 看支持哪些命令。${RESET}`);
    this.out("");
    this.prompt();
  }

  input(data: string) {
    let i = 0;
    while (i < data.length) {
      const ch = data[i];

      if (ch === "\u001b" && data[i + 1] === "[") {
        const code = data[i + 2];
        if (code === "A") this.recall(-1);
        else if (code === "B") this.recall(1);
        i += 3;
        continue;
      }

      if (ch === "\r" || ch === "\n") {
        this.out("");
        this.submit(this.buf);
        this.buf = "";
        this.histIdx = -1;
        i += 1;
        continue;
      }

      if (ch === "\u007f" || ch === "\b") {
        if (this.buf.length > 0) {
          this.buf = this.buf.slice(0, -1);
          this.write("\b \b");
        }
        i += 1;
        continue;
      }

      if (ch === "\u0003") {
        this.buf = "";
        this.out("^C");
        this.prompt();
        i += 1;
        continue;
      }

      if (ch === "\u000c") {
        this.write("\x1b[2J\x1b[H");
        this.prompt();
        i += 1;
        continue;
      }

      if (ch === "\t") {
        const done = this.complete();
        if (!done) this.write("\x07");
        i += 1;
        continue;
      }

      if (ch >= " ") {
        this.buf += ch;
        this.write(ch);
      }
      i += 1;
    }
  }

  private out(line = "") {
    this.write(`${line}\n`);
  }

  private help() {
    const rows: [string, string][] = [
      ["文件", "ls / ls -la / ll / cd <目录> / pwd / cat <文件> / head -n N / tail -n N / mkdir <目录>"],
      ["系统", "uptime / free -h / df -h / ps aux / ss -tlnp / uname -a / date / whoami / id / hostname"],
      ["服务", "systemctl status nginx|mysql / systemctl is-active <unit> / journalctl -u nginx -n 20"],
      ["nginx", "nginx -t / nginx -s reload"],
      ["容器", "docker ps [-a] / docker images / docker logs --tail N <名> / docker stats / docker start|stop|restart <名> / docker inspect <名>"],
      ["其它", "history / clear / echo / top（快照） / vim、nano（请用「文件」标签编辑） / exit（Tab 补全、↑↓ 历史都可用）"],
    ];
    this.out(`${BOLD}演示模式支持的命令${RESET}`);
    this.out("");
    for (const [k, v] of rows) this.out(`  ${YELLOW}${k.padEnd(6)}${RESET} ${v}`);
    this.out("");
    this.out(`${DIM}可用目录：~  /etc  /etc/nginx  /etc/nginx/conf.d  /data  /data/app  /data/app/src  /data/logs  /data/backup${RESET}`);
    this.out(`${DIM}有内置内容的文件：~/console.log、~/.bashrc、~/reset_snapshot.py、~/projects/notes.md、/etc/nginx/nginx.conf、/data/app/.env、/data/app/src/server.ts、/data/logs/app.log${RESET}`);
  }

  private prompt() {
    const shown = this.cwd.startsWith(HOME_DIR)
      ? `~${this.cwd.slice(HOME_DIR.length)}`
      : this.cwd;
    this.write(`${this.promptUser()}:${BLUE}${shown}${RESET}$ `);
  }

  private promptUser() {
    return `\x1b[32m${this.user}@${this.host}\x1b[0m`;
  }

  private recall(dir: number) {
    if (this.history.length === 0) return;
    if (this.histIdx === -1) this.histIdx = this.history.length;
    this.histIdx = Math.max(0, Math.min(this.history.length, this.histIdx + dir));
    this.write(`\r\x1b[K${this.promptUser()}:${BLUE}${this.shortCwd()}${RESET}$ `);
    this.buf = this.history[this.histIdx] ?? "";
    this.write(this.buf);
  }

  private shortCwd() {
    return this.cwd.startsWith(HOME_DIR)
      ? `~${this.cwd.slice(HOME_DIR.length)}`
      : this.cwd;
  }

  private complete(): boolean {
    const cmds = ["cat", "cd", "clear", "df", "docker", "echo", "exit", "free", "head", "help", "history", "hostname", "id", "journalctl", "ls", "nginx", "ps", "pwd", "ss", "sudo", "systemctl", "tail", "uname", "uptime", "whoami"];
    const parts = this.buf.split(/\s+/);
    if (parts.length !== 1) return false;
    const hits = cmds.filter((c) => c.startsWith(parts[0]));
    if (hits.length === 1) {
      const rest = hits[0].slice(parts[0].length);
      this.buf += rest;
      this.write(rest);
      return true;
    }
    if (hits.length > 1) {
      this.out("");
      this.out(hits.join("  "));
      this.prompt();
      this.write(this.buf);
      return true;
    }
    return false;
  }

  private submit(raw: string) {
    const line = raw.trim();
    if (line) {
      this.history.push(line);
      this.histIdx = -1;
    }
    const [cmd, ...args] = line.split(/\s+/);
    if (!cmd) {
      this.prompt();
      return;
    }

    const handled = this.run(cmd, args, line);
    if (handled !== "exit") this.prompt();
  }

  private run(cmd: string, args: string[], line: string): "ok" | "exit" {
    if (cmd === "sudo") {
      if (args.length === 0) {
        this.out(`${RED}usage: sudo command${RESET}`);
        return "ok";
      }
      return this.run(args[0], args.slice(1), line);
    }

    switch (cmd) {
      case "help":
        this.help();
        return "ok";

      case "clear":
        this.write("\x1b[2J\x1b[H");
        return "ok";

      case "exit":
      case "logout":
        this.out("logout");
        this.out(`${DIM}[连接已断开。演示模式，标签与连接保持不动]${RESET}`);
        return "exit";

      case "pwd":
        this.out(this.cwd);
        return "ok";

      case "whoami":
        this.out(this.user);
        return "ok";

      case "hostname":
        this.out(this.host);
        return "ok";

      case "id":
        this.out(`uid=1000(${this.user}) gid=1000(${this.user}) groups=1000(${this.user}),27(sudo),999(docker)`);
        return "ok";

      case "date":
        this.out(new Date().toString().replace("GMT+0800 (China Standard Time)", "CST"));
        return "ok";

      case "uname":
        this.out(`Linux ${this.host} 5.15.0-119-generic #129-Ubuntu SMP Wed Aug 21 12:00:00 UTC 2026 x86_64 GNU/Linux`);
        return "ok";

      case "uptime":
        this.out(` ${clock()} up 87 days,  3:41,  2 users,  load average: 2.14, 3.02, 2.66`);
        return "ok";

      case "cd":
        this.cd(args[0] ?? HOME_DIR);
        return "ok";

      case "mkdir": {
        const target = args.find((a) => !a.startsWith("-"));
        if (!target) {
          this.out(`${RED}mkdir: missing operand${RESET}`);
          return "ok";
        }
        const path = this.resolve(target);
        const parent = path.slice(0, path.lastIndexOf("/")) || "/";
        if (!fsTree[parent]) {
          this.out(`${RED}mkdir: cannot create directory '${target}': No such file or directory${RESET}`);
          return "ok";
        }
        if (fsTree[path]) {
          this.out(`${RED}mkdir: cannot create directory '${target}': File exists${RESET}`);
          return "ok";
        }
        fsTree[parent].push({
          name: path.slice(path.lastIndexOf("/") + 1),
          path,
          kind: "dir",
          size: 4096,
          mode: "drwxr-xr-x",
          owner: this.user,
          group: this.user,
          mtime: Date.now(),
          symlinkTarget: null,
        });
        fsTree[path] = [];
        return "ok";
      }

      case "ls":
      case "ll":
        this.ls(cmd === "ll" || args.includes("-la") || args.includes("-l") || args.includes("-al"));
        return "ok";

      case "cat":
        this.cat(args[0]);
        return "ok";

      case "head":
      case "tail": {
        const n = this.flagNum(args, "-n") ?? 10;
        this.headTail(args.filter((a) => !a.startsWith("-") && !/^\d+$/.test(a))[0], n, cmd === "tail");
        return "ok";
      }

      case "echo":
        this.out(args.join(" "));
        return "ok";

      case "history":
        this.history.forEach((h, i) => this.out(`  ${String(i + 1).padStart(4)}  ${h}`));
        return "ok";

      case "free":
        this.free();
        return "ok";

      case "df":
        this.df();
        return "ok";

      case "ps":
        this.ps();
        return "ok";

      case "ss":
        this.ss();
        return "ok";

      case "systemctl":
        this.systemctl(args);
        return "ok";

      case "journalctl":
        this.journalctl(args);
        return "ok";

      case "nginx":
        if (args[0] === "-t") {
          this.out("nginx: the configuration file /etc/nginx/nginx.conf syntax is ok");
          this.out("nginx: configuration file /etc/nginx/nginx.conf test is successful");
        } else if (args[0] === "-s" && args[1] === "reload") {
          this.out("nginx: reloaded");
        } else {
          this.out(`${RED}nginx: invalid option: "${args[0] ?? ""}"${RESET}`);
        }
        return "ok";

      case "docker":
        this.docker(args);
        return "ok";

      case "top":
      case "htop":
        this.out(`${DIM}（演示模式：交互式 TUI 需要有真实 TTY，这里用快照代替）${RESET}`);
        this.out("");
        this.out(`${BOLD}top - ${clock()} up 87 days,  3:41,  2 users,  load average: 2.14, 3.02, 2.66${RESET}`);
        this.out("Tasks: 214 total,   2 running, 212 sleeping,   0 stopped,   0 zombie");
        this.out("%Cpu(s): 18.3 us,  4.1 sy,  0.0 ni, 76.9 id,  0.6 wa,  0.0 hi,  0.1 si");
        this.out("MiB Mem :   7872.0 total,    422.5 free,   7468.7 used,    189.2 buff/cache");
        this.out("MiB Swap:   2048.0 total,     96.4 free,   1951.6 used.    172.3 avail Mem");
        this.out("");
        this.out("    PID USER      PR  NI    VIRT    RES    SHR S  %CPU  %MEM     TIME+ COMMAND");
        this.out("   1183 deploy    20   0  912.4m 402.1m  18.2m S  24.7   5.1  62:14.28 node");
        this.out("   1421 mysql     20   0  1.94g  1.42g  22.4m S  12.3  18.4 118:02.71 mysqld");
        this.out("      1 root      20   0  168.2m  12.4m   8.9m S   0.3   0.2   4:41.02 systemd");
        return "ok";

      case "vim":
      case "vi":
      case "nano":
        this.out(`${DIM}（演示模式：${cmd} 需要有真实 TTY；编辑文件请在「文件」标签里改）${RESET}`);
        return "ok";

      default:
        this.out(`bash: ${cmd}: command not found`);
        this.out(`${DIM}输入 help 查看演示模式支持的命令${RESET}`);
        return "ok";
    }
  }

  private flagNum(args: string[], flag: string): number | null {
    const i = args.indexOf(flag);
    if (i >= 0 && args[i + 1]) return Number(args[i + 1]);
    const inline = args.find((a) => a.startsWith(`${flag}=`));
    if (inline) return Number(inline.slice(flag.length + 1));
    return null;
  }

  private resolve(p?: string): string {
    if (!p) return this.cwd;
    if (p.startsWith("/")) return p.replace(/\/+$/, "") || "/";
    if (p === "~") return HOME_DIR;
    if (p.startsWith("~/")) return `${HOME_DIR}/${p.slice(2)}`;
    if (p === "..") {
      const parts = this.cwd.split("/").filter(Boolean);
      parts.pop();
      return parts.length ? `/${parts.join("/")}` : "/";
    }
    if (p === ".") return this.cwd;
    return this.cwd === "/" ? `/${p}` : `${this.cwd}/${p}`;
  }

  private cd(target: string) {
    const next = this.resolve(target);
    const exists = next === "/" || fsTree[next] !== undefined || Object.keys(fsTree).some((k) => k.startsWith(`${next}/`));
    if (!exists) {
      this.out(`bash: cd: ${target}: No such file or directory`);
      return;
    }
    this.cwd = next;
  }

  private ls(long: boolean) {
    const entries = fsTree[this.cwd];
    if (!entries) {
      this.out(`${DIM}（演示模式只准备了部分目录，试试 /、/etc、/etc/nginx、/data、/data/app、/data/logs）${RESET}`);
      return;
    }
    if (long) {
      this.out(`total ${entries.length * 4}`);
      for (const e of entries) {
        const name = e.kind === "dir" ? `${BOLD}${BLUE}${e.name}${RESET}` : e.name;
        this.out(
          `${e.mode} ${String(e.owner).padEnd(7)} ${String(e.group).padEnd(7)} ${String(e.size).padStart(9)} ${new Date(e.mtime).toDateString().slice(4, 10)} ${clock(Date.now() - e.mtime)} ${name}`,
        );
      }
      return;
    }
    const cells = entries.map((e) =>
      e.kind === "dir" ? `${BOLD}${BLUE}${e.name}${RESET}` : /\.(sh|bin)$/.test(e.name) ? `${GREEN}${e.name}${RESET}` : e.name,
    );
    for (let i = 0; i < cells.length; i += 4) {
      this.out(cells.slice(i, i + 4).join("    "));
    }
  }

  private cat(target?: string) {
    if (!target) {
      this.out(`${RED}cat: missing operand${RESET}`);
      return;
    }
    const path = this.resolve(target);
    const content = fsFileContent[path];
    if (content) {
      content.replace(/\n$/, "").split("\n").forEach((l) => this.out(l));
      return;
    }
    const entry = (fsTree[this.cwd] ?? []).find((e) => e.name === target);
    if (entry?.kind === "dir") {
      this.out(`${RED}cat: ${target}: Is a directory${RESET}`);
      return;
    }
    this.out(`${DIM}（演示模式：${path} 没有内置内容。有内置内容的是 /etc/nginx/nginx.conf、
/data/app/docker-compose.yml、/data/app/.env、/data/app/README.md、/data/logs/app.log 等）${RESET}`);
  }

  private headTail(target: string | undefined, n: number, tail: boolean) {
    if (!target) {
      this.out(`${RED}missing file operand${RESET}`);
      return;
    }
    const path = this.resolve(target);
    const content = fsFileContent[path] ?? fsFileContent[`${path}`];
    if (!content) {
      this.out(`${DIM}（演示模式：${path} 没有内置内容）${RESET}`);
      return;
    }
    const lines = content.replace(/\n$/, "").split("\n");
    const picked = tail ? lines.slice(-n) : lines.slice(0, n);
    picked.forEach((l) => this.out(l));
  }

  private free() {
    this.out("               total        used        free      shared  buff/cache   available");
    this.out("Mem:           7.7Gi       7.3Gi       380Mi        12Mi       189Mi       172Mi");
    this.out("Swap:          2.0Gi       1.9Gi       104Mi");
  }

  private df() {
    this.out("Filesystem      Size  Used Avail Use% Mounted on");
    this.out("/dev/vda1        99G   63G   31G  68% /");
    this.out("tmpfs           3.9G     0  3.9G   0% /dev/shm");
    this.out("/dev/vdb1       197G  142G   45G  77% /data");
    this.out("overlay         197G  142G   45G  77% /var/lib/docker/overlay2/9a1f2c");
  }

  private ps() {
    this.out("USER         PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND");
    this.out("root           1  0.0  0.2 168216 12404 ?        Ss   Sep01   4:41 /sbin/init");
    this.out("root        1183 24.7  5.1 934297 412180 ?       Ssl  09:12  62:14 node dist/server.js");
    this.out("mysql       1421 12.3 18.4 2034892 1483120 ?   Ssl  09:12 118:02 mysqld");
    this.out("root        4721  0.1  0.3 1123456 24812 ?      Sl   14:20   0:18 docker-proxy");
    this.out("deploy      8812  0.0  0.1  12840  4120 pts/0    Ss   16:21   0:00 -bash");
    this.out("deploy      8844  0.0  0.1  14412  3864 pts/0    R+   16:31   0:00 ps aux");
  }

  private ss() {
    this.out("State  Recv-Q Send-Q Local Address:Port  Peer Address:Port Process");
    this.out("LISTEN 0      511          0.0.0.0:80         0.0.0.0:*     users:((\"nginx\",pid=1102,fd=6))");
    this.out("LISTEN 0      511          0.0.0.0:443        0.0.0.0:*     users:((\"nginx\",pid=1102,fd=7))");
    this.out("LISTEN 0      4096       127.0.0.1:8080       0.0.0.0:*     users:((\"node\",pid=1183,fd=19))");
    this.out("LISTEN 0      511        127.0.0.1:6379       0.0.0.0:*     users:((\"redis\",pid=1340,fd=6))");
    this.out("LISTEN 0      151        127.0.0.1:3306       0.0.0.0:*     users:((\"mysqld\",pid=1421,fd=22))");
    this.out("LISTEN 0      4096             *:22               *:*     users:((\"sshd\",pid=902,fd=3))");
  }

  private systemctl(args: string[]) {
    const sub = args[0];
    const unit = args[1] ?? "nginx";
    if (sub === "status") {
      if (unit.startsWith("mysql")) {
        this.out(`${RED}●${RESET} mysql.service - MySQL Community Server`);
        this.out(`     Loaded: loaded (/lib/systemd/system/mysql.service; enabled)`);
        this.out(`     Active: ${RED}failed (Result: oom-kill)${RESET} since Sat 2026-09-26 ${clock(12 * 60_000)} CST; 12min ago`);
        this.out(`   Main PID: 1421 (code=killed, signal=KILL)`);
        return;
      }
      this.out(`${GREEN}●${RESET} nginx.service - A high performance web server and a reverse proxy server`);
      this.out(`     Loaded: loaded (/lib/systemd/system/nginx.service; enabled; preset: enabled)`);
      this.out(`     Active: ${GREEN}active (running)${RESET} since Sat 2026-09-26 09:12:04 CST; 7h ago`);
      this.out(`   Main PID: 1102 (nginx)`);
      this.out(`      Tasks: 5 (limit: 4574)`);
      this.out(`     Memory: 12.4M`);
      return;
    }
    if (sub === "is-active") {
      this.out(unit.startsWith("mysql") ? `${RED}failed${RESET}` : `${GREEN}active${RESET}`);
      return;
    }
    if (sub === "restart" || sub === "start" || sub === "stop") {
      this.out(`${YELLOW}（演示模式：未模拟 systemctl ${sub}，没有做任何变更）${RESET}`);
      return;
    }
    this.out(`${RED}Unknown operation ${sub}.${RESET}`);
  }

  private journalctl(args: string[]) {
    const n = this.flagNum(args, "-n") ?? 20;
    const unit = args[args.indexOf("-u") + 1] ?? "nginx";
    this.out(`${DIM}-- Journal begins at Mon 2026-06-30 08:12:04 CST, ends at Sat 2026-09-26 ${clock()} CST. --${RESET}`);
    const lines = [
      `Sep 26 09:12:04 web-01 systemd[1]: Starting ${unit}...`,
      `Sep 26 09:12:04 web-01 systemd[1]: Started ${unit}.`,
      `Sep 26 10:03:11 web-01 ${unit}[1102]: 10.0.0.8 - - [26/Sep/2026:10:03:11 +0800] "GET /api/products HTTP/1.1" 200 1832`,
      `Sep 26 14:20:55 web-01 ${unit}[1102]: 10.0.0.21 - - [26/Sep/2026:14:20:55 +0800] "GET /api/orders HTTP/1.1" 502 154`,
      `Sep 26 15:41:02 web-01 ${unit}[1102]: upstream timed out (110: Connection timed out) while reading response header from upstream`,
      `Sep 26 16:14:33 web-01 ${unit}[1102]: 10.0.0.8 - - [26/Sep/2026:16:14:33 +0800] "GET /api/orders HTTP/1.1" 502 154`,
      `Sep 26 16:22:07 web-01 ${unit}[1102]: upstream timed out (110: Connection timed out) while reading response header from upstream`,
    ];
    lines.slice(-n).forEach((l) => this.out(l));
  }

  private docker(args: string[]) {
    const sub = args[0];
    if (sub === "ps") {
      const all = args.includes("-a") || args.includes("--all");
      this.out(`${BOLD}CONTAINER ID   IMAGE                              COMMAND                  CREATED        STATUS                     PORTS                                   NAMES${RESET}`);
      for (const c of containers) {
        if (!all && c.state !== "running") continue;
        const status = c.state === "running" ? `${GREEN}${c.status}${RESET}` : `${RED}${c.status}${RESET}`;
        this.out(
          `${c.id.slice(0, 12)}   ${c.image.padEnd(33)} "node dist/server.js"    ${c.state === "running" ? "3 hours ago" : "2 months ago"}   ${status.padEnd(38)} ${(c.ports || "").padEnd(38)} ${c.name}`,
        );
      }
      return;
    }
    if (sub === "images") {
      this.out(`${BOLD}REPOSITORY                          TAG                 IMAGE ID       CREATED         SIZE${RESET}`);
      const rows: [string, string, string, string, string][] = [
        ["nexterm/api", "2.4.1", "1a2b3c4d5e6f", "3 days ago", "212MB"],
        ["mysql", "8.0", "2b3c4d5e6f7a", "2 weeks ago", "591MB"],
        ["redis", "7-alpine", "3c4d5e6f7a8b", "5 weeks ago", "41.2MB"],
        ["nginx", "1.27-alpine", "4d5e6f7a8b9c", "1 month ago", "48.9MB"],
      ];
      rows.forEach((r) => this.out(`${r[0].padEnd(35)} ${r[1].padEnd(19)} ${r[2]}   ${r[3].padEnd(15)} ${r[4]}`));
      return;
    }
    if (sub === "logs") {
      const name = args.find((a) => !a.startsWith("-"));
      const tail = this.flagNum(args, "--tail") ?? 12;
      if (!name) {
        this.out(`${RED}"docker logs" requires exactly 1 argument.${RESET}`);
        return;
      }
      const lines = containerLogLines(name);
      lines.slice(-tail).forEach((l) => this.out(l));
      return;
    }
    if (sub === "stats") {
      this.out(`${BOLD}CONTAINER ID   NAME             CPU %     MEM USAGE / LIMIT     MEM %     NET I/O           BLOCK I/O${RESET}`);
      const rows: [string, string, string, string, string, string][] = [
        ["c1a2b3d4e5f6", "api-server", "30.42%", "1.412GiB / 2GiB", "70.61%", "84.2MB / 21.4MB"],
        ["c2b3c4d5e6f7", "kingbase-pg", "0.21%", "293.5MiB / 7.68GiB", "3.73%", "12.1MB / 4.9MB"],
        ["c4d5e6f7a8b9", "redis-cache", "1.08%", "48.2MiB / 512MiB", "9.41%", "221MB / 189MB"],
        ["c6f7a8b9c0d1", "nginx-gateway", "0.31%", "18.7MiB / 7.68GiB", "0.24%", "1.8GB / 1.7GB"],
      ];
      rows.forEach((r) => this.out(`${r[0].slice(0, 12)}   ${r[1].padEnd(16)} ${r[2].padEnd(9)} ${r[3].padEnd(21)} ${r[4].padEnd(9)} ${r[5]}`));
      return;
    }
    if (sub === "restart" || sub === "start" || sub === "stop") {
      const name = args[1];
      const c = containers.find((x) => x.name === name);
      if (!c) {
        this.out(`Error response from daemon: No such container: ${name}`);
        return;
      }
      if (sub === "stop") {
        c.state = "exited";
        c.status = "Exited (0) Less than a second ago";
        this.out(name);
        this.out(`${DIM}（Docker 面板刷新后能看到状态变化）${RESET}`);
        return;
      }
      c.state = "running";
      c.status = "Up Less than a second";
      this.out(name);
      this.out(`${DIM}（Docker 面板刷新后能看到状态变化）${RESET}`);
      return;
    }
    if (sub === "inspect") {
      this.out("[");
      this.out("    {");
      this.out(`        "Id": "${containers[0].id}${"0".repeat(52)}",`);
      this.out('        "Name": "/api-server",');
      this.out('        "State": { "Status": "running", "Running": true, "Pid": 1183, "RestartCount": 0 },');
      this.out('        "Config": { "Image": "nexterm/api:2.4.1", "Env": ["NODE_ENV=production"] }');
      this.out("    }");
      this.out("]");
      return;
    }
    if (sub === "exec") {
      this.out(`${DIM}（演示模式：docker exec -it 用「容器」面板里的「终端」按钮打开）${RESET}`);
      return;
    }
    this.out(`docker: '${sub ?? ""}' is not a docker command.`);
  }
}

export function containerLogLines(name: string): string[] {
  const t = (offsetMin: number) => new Date(Date.now() - offsetMin * 60_000).toISOString().replace("T", " ").slice(0, 19);
  if (name === "api-server") {
    return [
      `${t(26)} INFO  server started, pid=1183 port=8080`,
      `${t(25)} INFO  redis connected (redis-cache:6379)`,
      `${t(25)} INFO  mysql pool ready, size=10`,
      `${t(18)} WARN  pool exhausted, waiting for a free connection (queued=7)`,
      `${t(16)} WARN  pool exhausted, waiting for a free connection (queued=23)`,
      `${t(12)} ERROR read ECONNRESET from mysql-prod:3306`,
      `${t(12)} ERROR db connection lost, retrying in 1000ms`,
      `${t(11)} ERROR db connection lost, retrying in 2000ms`,
      `${t(9)}  WARN  upstream 502 for /api/products (db unavailable)`,
      `${t(3)}  WARN  upstream 502 for /api/orders`,
      `${t(1)}  INFO  db connection re-established after 3 attempts`,
    ];
  }
  if (name === "mysql-prod") {
    return [
      `${t(9)} [Note] [Entrypoint]: Starting temporary server`,
      `${t(8)} [Warning] InnoDB: innodb_buffer_pool_size=1.4G is too large for 7.7G system`,
      `${t(6)} [Warning] InnoDB: Cannot allocate 134217728 bytes, retrying`,
      `${t(3)} [ERROR] Out of memory: Kill process 1421 (mysqld) score 941 or sacrifice child`,
      `${t(3)} [ERROR] mysqld got signal 9; This could be because you hit a bug.`,
      `${t(3)} [Note] mysqld: Shutdown complete (exit code 137)`,
    ];
  }
  if (name === "redis-cache") {
    return [
      `${t(180)} * Ready to accept connections tcp`,
      `${t(120)} * 100 changes in 300 seconds. Saving...`,
      `${t(120)} * Background saving started by pid 42`,
      `${t(119)} * Background saving terminated with success`,
      `${t(2)}   * 1 changes in 3600 seconds. Saving...`,
    ];
  }
  if (name === "nginx-gateway") {
    const base = Math.floor(Date.now() / 1000);
    return Array.from({ length: 14 }, (_, i) => {
      const ok = i % 4 !== 3;
      const path = ["/api/products", "/api/orders", "/api/users/me", "/healthz"][i % 4];
      return `10.0.0.${20 + (i % 5)} - - [${new Date().toLocaleDateString("en-GB").replace(/\//g, "/")}] "GET ${path} HTTP/1.1" ${ok ? 200 : 502} ${ok ? 1832 : 154} "-" "curl/8.5.0" (${base - i * 7}.${(i * 137) % 1000})`;
    });
  }
  return [`${t(5)} 容器 ${name} 暂无日志输出`];
}

export const demoRedisKeyList = redisKeys;
