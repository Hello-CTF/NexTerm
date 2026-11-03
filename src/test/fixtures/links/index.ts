// Constructed (recorded: false) terminal output shaped like the real tools'
// output; there is no live login environment to record from. `data` is what is
// written to the terminal, with explicit hard line breaks.

export interface LinkFixtureUrl {
  url: string;
  /** Expected URL text on each buffer row it spans, in order (no border, no prefix). */
  parts: string[];
}

export interface LinkFixture {
  name: string;
  recorded: false;
  cols: number;
  data: string;
  urls: LinkFixtureUrl[];
}

export function hardWrap(text: string, width: number): string[] {
  const out: string[] = [];
  for (let i = 0; i < text.length; i += width) out.push(text.slice(i, i + width));
  return out;
}

/** Parts of a URL that xterm soft-wraps when it starts at `offset` on a row of `cols` cells. */
export function softParts(offset: number, url: string, cols: number): string[] {
  const parts: string[] = [];
  let pos = 0;
  let room = cols - offset;
  while (pos < url.length) {
    parts.push(url.slice(pos, pos + room));
    pos += room;
    room = cols;
  }
  return parts;
}

const CHALLENGE = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM";
const STATE = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk";

export const OAUTH_URL =
  "https://claude.ai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e" +
  "&response_type=code&redirect_uri=https%3A%2F%2Fconsole.anthropic.com%2Foauth%2Fcode%2Fcallback" +
  "&scope=org%3Acreate_api_key+user%3Aprofile+user%3Ainference" +
  `&code_challenge=${CHALLENGE}&code_challenge_method=S256&state=${STATE}`;

const NL = "\r\n";

function claudeLogin(): LinkFixture {
  const cols = 80;
  const parts = hardWrap(OAUTH_URL, cols);
  const data = [" Browser didn't open? Use the url below to sign in (c to copy)", "", ...parts, "", " Paste code here if prompted >"].join(NL);
  return { name: "claude-code-login", recorded: false, cols, data, urls: [{ url: OAUTH_URL, parts }] };
}

function boxed(width: number, termCols: number, name: string): LinkFixture {
  const inner = width - 4;
  const parts = hardWrap(OAUTH_URL, inner);
  const row = (s: string) => `│ ${s.padEnd(inner)} │`;
  const data = [
    `╭${"─".repeat(width - 2)}╮`,
    row("Browser didn't open? Use the url below"),
    row(""),
    ...parts.map(row),
    row(""),
    row("Paste code here if prompted >"),
    `╰${"─".repeat(width - 2)}╯`,
  ].join(NL);
  return { name, recorded: false, cols: termCols, data, urls: [{ url: OAUTH_URL, parts }] };
}

const DEVICE_URL = "https://github.com/login/device";

function ghLogin(cols: number, name: string): LinkFixture {
  const prefix = "Press Enter to open ";
  const data = [
    "? What account do you want to log into? GitHub.com",
    "? What is your preferred protocol for Git operations on this host? HTTPS",
    "? Authenticate Git with your GitHub credentials? Yes",
    "? How would you like to authenticate GitHub CLI? Login with a web browser",
    "",
    "! First copy your one-time code: 1A2B-3C4D",
    `${prefix}${DEVICE_URL} in your browser... `,
  ].join(NL);
  return { name, recorded: false, cols, data, urls: [{ url: DEVICE_URL, parts: softParts(prefix.length, DEVICE_URL, cols) }] };
}

const NPM_LOGIN_URL = "https://www.npmjs.com/login?next=/login/cli/8c3f1b2e-4d5a-4b6c-9e7f-0a1b2c3d4e5f";

function npmLogin(): LinkFixture {
  const cols = 60;
  const data = [
    "npm notice Log in on https://registry.npmjs.org/",
    "Login at:",
    NPM_LOGIN_URL,
    "Press ENTER to open in the browser...",
  ].join(NL);
  return {
    name: "npm-login",
    recorded: false,
    cols,
    data,
    urls: [
      { url: "https://registry.npmjs.org/", parts: ["https://registry.npmjs.org/"] },
      { url: NPM_LOGIN_URL, parts: softParts(0, NPM_LOGIN_URL, cols) },
    ],
  };
}

const PLAIN_URL =
  "https://auth.example.com/device/authorize?user_code=WDJB-MJHT&client_id=0a1b2c3d4e5f&redirect_uri=https%3A%2F%2Fapp.example.com%2Fcb";

function quoted(): LinkFixture {
  const cols = 60;
  const parts = hardWrap(PLAIN_URL, cols - 2);
  const data = ["> Open this link to continue:", ...parts.map((p) => `> ${p}`), "", "done"].join(NL);
  return { name: "quote-prefix", recorded: false, cols, data, urls: [{ url: PLAIN_URL, parts }] };
}

function numbered(): LinkFixture {
  const cols = 60;
  const parts = hardWrap(PLAIN_URL, cols - 7);
  const data = ["  11 │ // see:", ...parts.map((p, i) => `  ${12 + i} │ ${p}`), `  ${12 + parts.length} │ done`].join(NL);
  return { name: "line-number-prefix", recorded: false, cols, data, urls: [{ url: PLAIN_URL, parts }] };
}

export const LINK_FIXTURES: LinkFixture[] = [
  claudeLogin(),
  boxed(84, 100, "claude-code-login-boxed"),
  ghLogin(100, "gh-auth-login"),
  ghLogin(40, "gh-auth-login-narrow"),
  npmLogin(),
  quoted(),
  numbered(),
];
