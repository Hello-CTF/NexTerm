import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

const stringRestrictions = (pattern, message) => [
  { selector: `Literal[value=/${pattern}/]`, message },
  { selector: `TemplateElement[value.raw=/${pattern}/]`, message },
];

const themedTextRestrictions = [
  ...stringRestrictions(
    String.raw`\btext-(?:amber-100|amber-200|blue-200|emerald-\w+)`,
    "Use semantic theme foreground tokens instead of low-contrast text colors.",
  ),
  ...stringRestrictions(
    String.raw`\btext-\[#[0-9a-fA-F]{3,8}\]`,
    "Use semantic theme foreground tokens instead of arbitrary hex text colors.",
  ),
];

const neutral600Restrictions = (...allowedValues) => {
  const literalAllowances = allowedValues
    .map((value) => `:not([value=${JSON.stringify(value)}])`)
    .join("");
  const templateAllowances = allowedValues
    .map((value) => `:not([value.raw=${JSON.stringify(value)}])`)
    .join("");
  const message = "Use neutral-500 for readable metadata; reserve neutral-600 for decorative content.";
  return [
    {
      selector: `Literal[value=/\\btext-neutral-600\\b/]${literalAllowances}`,
      message,
    },
    {
      selector: `TemplateElement[value.raw=/\\btext-neutral-600\\b/]${templateAllowances}`,
      message,
    },
  ];
};

const themedSurfaceRestrictions = (...allowedNeutral600) => [
  ...themedTextRestrictions,
  ...neutral600Restrictions(...allowedNeutral600),
];

const credentialAmberRestrictions = stringRestrictions(
  String.raw`\btext-amber-(?:400|500)\b`,
  "Use the semantic warning foreground instead of low-contrast amber text.",
);

export default tseslint.config(
  { ignores: ["dist", "cmd/nexterm-desktop/dist", ".buildcheck", ".sitetest", "_site", ".tower/worktrees", ".claude/worktrees", ".tmp", ".task", "lazycat", "reference", "target", "*.cjs", "src/ipc/bindings"] },
  tseslint.configs.recommended,
  {
    plugins: { "react-hooks": reactHooks },
    rules: {
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "warn",
    },
  },
  {
    rules: {
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
      "@typescript-eslint/consistent-type-imports": [
        "error",
        { fixStyle: "inline-type-imports", disallowTypeAnnotations: false },
      ],
      "@typescript-eslint/default-param-last": "error",
      "@typescript-eslint/no-non-null-asserted-nullish-coalescing": "error",
      "@typescript-eslint/no-useless-empty-export": "error",
      "no-console": ["error", { allow: ["warn", "error"] }],
    },
  },
  {
    // The store slices import each other in a cycle that is only safe when src/app/store.ts is the entry point.
    files: ["src/**/*.{ts,tsx}"],
    ignores: ["src/app/store.ts", "src/app/store/**"],
    rules: {
      "no-restricted-imports": [
        "error",
        { patterns: [{ group: ["**/app/store/*", "./store/*"], message: "Import the UI store through src/app/store.ts." }] },
      ],
    },
  },
  {
    files: ["src/**/*.{ts,tsx}"],
    languageOptions: {
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    rules: {
      "@typescript-eslint/await-thenable": "error",
      "@typescript-eslint/consistent-type-exports": "error",
      "@typescript-eslint/no-array-delete": "error",
      "@typescript-eslint/no-duplicate-type-constituents": "error",
      "@typescript-eslint/no-floating-promises": "warn",
      "@typescript-eslint/no-for-in-array": "error",
      "@typescript-eslint/no-misused-promises": "warn",
      "@typescript-eslint/no-unsafe-enum-comparison": "error",
      "@typescript-eslint/require-array-sort-compare": "error",
      "@typescript-eslint/restrict-template-expressions": "error",
    },
  },
  {
    files: ["src/**/*.tsx"],
    rules: {
      "no-restricted-syntax": ["error", ...themedSurfaceRestrictions()],
    },
  },
  {
    files: ["src/features/sharing/PublicPreviewApp.tsx", "src/features/sharing/PublicShareApp.tsx"],
    rules: {
      "no-restricted-syntax": "off",
    },
  },
  {
    files: ["src/app/App.tsx"],
    rules: {
      "no-restricted-syntax": [
        "error",
        ...themedSurfaceRestrictions("text-neutral-600"),
        ...stringRestrictions(
          String.raw`#[0-9a-fA-F]{3,8}`,
          "Use semantic theme tokens instead of raw hex colors in App.",
        ),
        ...stringRestrictions(
          String.raw`rgba?\(`,
          "Use semantic theme tokens instead of raw RGB colors in App.",
        ),
        ...stringRestrictions(
          String.raw`(?:^|\s)shadow-pop(?:$|\s)`,
          "Use shadow-[var(--shadow-pop)] so the runtime theme controls the shadow.",
        ),
      ],
    },
  },
  {
    files: ["src/ui/editorTheme.ts"],
    rules: {
      "no-restricted-syntax": [
        "error",
        ...stringRestrictions(
          String.raw`#[0-9a-fA-F]{6}`,
          "Reference the nx-cm theme slots instead of raw hex colors.",
        ),
      ],
    },
  },
  {
    files: [
      "src/features/credentials/CredentialsSidebar.tsx",
      "src/features/credentials/NewCredentialModal.tsx",
    ],
    rules: {
      "no-restricted-syntax": ["error", ...themedSurfaceRestrictions(), ...credentialAmberRestrictions],
    },
  },
  {
    files: ["src/features/credentials/CredentialsPanel.tsx"],
    rules: {
      "no-restricted-syntax": [
        "error",
        ...themedSurfaceRestrictions("font-mono text-[13px] tracking-[0.3em] text-neutral-600"),
        ...credentialAmberRestrictions,
      ],
    },
  },
  {
    files: [
      "src/features/forward/ForwardPanel.tsx",
      "src/features/files/FileTree.tsx",
      "src/features/docker/DockerPanel.tsx",
      "src/features/settings/CommandLogView.tsx",
      "src/features/settings/AuditView.tsx",
    ],
    rules: {
      "no-restricted-syntax": ["error", ...themedSurfaceRestrictions("text-neutral-600")],
    },
  },
  {
    files: ["src/features/files/LogViewer.tsx"],
    rules: {
      "no-restricted-syntax": [
        "error",
        ...themedSurfaceRestrictions("w-12 shrink-0 select-none text-right text-neutral-600"),
      ],
    },
  },
  {
    files: ["src/features/docker/ContainerInsight.tsx"],
    rules: {
      "no-restricted-syntax": [
        "error",
        ...themedSurfaceRestrictions(
          "text-neutral-600",
          "mr-1.5 inline align-[-2px] text-neutral-600",
        ),
      ],
    },
  },
  {
    files: ["src/features/files/MountPanel.tsx"],
    rules: {
      "no-restricted-syntax": [
        "error",
        ...themedSurfaceRestrictions("text-neutral-600", "mx-auto mb-3 text-neutral-600"),
      ],
    },
  },
  {
    files: ["src/features/explorer/AssetTree.tsx"],
    rules: {
      "no-restricted-syntax": ["error", ...themedSurfaceRestrictions("shrink-0 text-neutral-600")],
    },
  },
  {
    files: ["src/features/terminal/WorkspaceRestoreBanner.tsx"],
    rules: {
      "no-restricted-syntax": ["warn", ...themedSurfaceRestrictions()],
    },
  },
  {
    files: ["scripts/**/*.mjs"],
    rules: {
      "no-console": "off",
    },
  },
);
