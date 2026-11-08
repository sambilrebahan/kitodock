import Editor, { DiffEditor } from "@monaco-editor/react";

// Monaco-based document editor with a diff view for conflicts.
export default function DocumentEditor({
  value,
  language,
  onChange,
  onSave,
  readOnly,
}: {
  value: string;
  language: string;
  onChange?: (v: string) => void;
  onSave?: () => void;
  readOnly?: boolean;
}) {
  return (
    <Editor
      defaultLanguage={language}
      language={language}
      value={value}
      theme="vs-dark"
      onChange={(v) => onChange?.(v ?? "")}
      options={{
        minimap: { enabled: false },
        wordWrap: "on",
        fontSize: 13,
        fontFamily: "JetBrains Mono, Menlo, monospace",
        lineNumbers: "on",
        scrollBeyondLastLine: false,
        readOnly,
        automaticLayout: true,
        tabSize: 4,
      }}
    />
  );
}

// ConflictDiff shows the original vs modified side-by-side for a save
// conflict, letting the user merge manually.
export function ConflictDiff({
  original,
  modified,
  language,
}: {
  original: string;
  modified: string;
  language: string;
}) {
  return (
    <DiffEditor
      original={original}
      modified={modified}
      language={language}
      theme="vs-dark"
      options={{
        renderSideBySide: true,
        readOnly: false,
        automaticLayout: true,
        minimap: { enabled: false },
      }}
    />
  );
}
