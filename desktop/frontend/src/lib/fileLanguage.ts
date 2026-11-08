// Maps a file extension to a Monaco language id.
const MAP: Record<string, string> = {
  ts: "typescript", tsx: "typescript", js: "javascript", jsx: "javascript",
  json: "json", md: "markdown", css: "css", scss: "scss", less: "less",
  html: "html", htm: "html", xml: "xml", svg: "xml",
  php: "php", blade: "html", vue: "html",
  py: "python", rb: "ruby", go: "go", rs: "rust", java: "java",
  c: "c", h: "c", cpp: "cpp", hpp: "cpp", cs: "csharp",
  yml: "yaml", yaml: "yaml", toml: "ini", ini: "ini",
  sql: "sql", sh: "shell", bat: "bat", ps1: "powershell",
  env: "ini", gitignore: "ini", dockerfile: "dockerfile",
  txt: "plaintext", log: "plaintext",
};

export function fileLanguage(path: string): string {
  const name = path.split("/").pop() ?? "";
  const dot = name.lastIndexOf(".");
  if (dot < 0) return name.toLowerCase() === "dockerfile" ? "dockerfile" : "plaintext";
  return MAP[name.slice(dot + 1).toLowerCase()] ?? "plaintext";
}
