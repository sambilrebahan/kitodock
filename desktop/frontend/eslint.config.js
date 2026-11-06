import reactHooks from "eslint-plugin-react-hooks";
import tseslint from "typescript-eslint";

// TypeScript syntax needs the TS parser; hook discipline is the one rule set
// enforced. Broader style rules can join when there is a codebase-shaped
// reason for them — starting strict-but-small keeps lint a tool, not a tax.
export default tseslint.config(
  { ignores: ["dist", "wailsjs"] },
  {
    files: ["src/**/*.{ts,tsx}"],
    languageOptions: {
      parser: tseslint.parser,
      parserOptions: { ecmaFeatures: { jsx: true } },
    },
    plugins: { "react-hooks": reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
    },
  },
);
