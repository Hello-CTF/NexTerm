import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["dist", "cmd/nexterm-desktop/dist", ".buildcheck", ".sitetest", ".tower/worktrees", "lazycat", "reference", "target", "scripts", "*.cjs", "src/ipc/bindings"] },
  tseslint.configs.recommended,
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
      "no-console": ["error", { allow: ["warn", "error"] }],
    },
  },
);
