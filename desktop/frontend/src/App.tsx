import { useState } from "react";
import Sidebar, { type Page } from "./components/Sidebar";
import WindowControls from "./components/WindowControls";
import Agents from "./pages/Agents";
import Cron from "./pages/Cron";
import Dashboard from "./pages/Dashboard";
import Deployments from "./pages/Deployments";
import Downloads from "./pages/Downloads";
import Projects from "./pages/Projects";
import Settings from "./pages/Settings";
import Ssh from "./pages/Ssh";
import TerminalPage from "./pages/TerminalPage";

// Phase 0 runs a single main window. The detached-terminal and command-bar
// personalities join in Phases 3 and 6; the shape of this component is
// already prepared for them.
export default function App() {
  return <MainApp />;
}

function MainApp() {
  const [page, setPage] = useState<Page>("dashboard");

  // Keep-alive pages: every page stays mounted and is toggled with
  // display:none, so terminal state, scroll positions and form inputs
  // survive navigation. No router on purpose — see docs/02 §6.
  const pages: { id: Page; element: JSX.Element }[] = [
    { id: "dashboard", element: <Dashboard /> },
    { id: "projects", element: <Projects /> },
    { id: "terminal", element: <TerminalPage /> },
    { id: "ssh", element: <Ssh /> },
    { id: "cron", element: <Cron /> },
    { id: "deployments", element: <Deployments /> },
    { id: "downloads", element: <Downloads /> },
    { id: "agents", element: <Agents /> },
    { id: "settings", element: <Settings /> },
  ];

  return (
    <div className="flex h-full w-full flex-col">
      {/* Frameless drag strip: the style attribute is what Wails reads for
          window dragging; buttons inside it still work because Wails only
          starts a drag on the element that carries the attribute. */}
      <div
        data-titlebar
        style={{ "--wails-draggable": "drag" } as React.CSSProperties}
        className="flex h-9 shrink-0 items-center justify-end pl-3 select-none"
      >
        <WindowControls />
      </div>
      <div className="flex min-h-0 flex-1">
        <Sidebar page={page} onNavigate={setPage} />
        <main className="min-w-0 flex-1 overflow-y-auto p-4">
          {pages.map(({ id, element }) => (
            <div key={id} style={{ display: page === id ? "block" : "none" }} className="h-full">
              {element}
            </div>
          ))}
        </main>
      </div>
    </div>
  );
}
