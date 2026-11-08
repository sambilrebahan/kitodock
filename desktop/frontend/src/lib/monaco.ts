// Redirect the Monaco loader to the locally vendored copy (public/vs) so
// the editor never touches the CDN.
import { loader } from "@monaco-editor/react";

// @ts-ignore — the vendored monaco is a plain script tag global.
loader.config({ paths: { vs: "/vs" } });
