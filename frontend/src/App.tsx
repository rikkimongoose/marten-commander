import { useRef, useState } from "react";
import { Alert, Box, CssBaseline } from "@mui/material";
import { AgGridProvider } from "ag-grid-react";
import { AllCommunityModule } from "ag-grid-community";
import { FilePanel, type FilePanelHandle } from "./FilePanel";
import { CommandDispatcher, CommandError, commandErrorMessage, sourceAndTarget, type CommandId, type PanelId } from "./commands";

export default function App() {
  const [active, setActive] = useState<PanelId>("left");
  const activeRef = useRef<PanelId>("left");
  const left = useRef<FilePanelHandle>(null);
  const right = useRef<FilePanelHandle>(null);
  const [commandError, setCommandError] = useState("");
  const [commands] = useState(() => {
    const empty = () => ({ directoryLocation: null, selectedLocations: [], focusedLocation: null });
    const dispatcher = new CommandDispatcher(() => ({
      activePanel: activeRef.current,
      left: left.current?.getContext() ?? empty(),
      right: right.current?.getContext() ?? empty(),
    }));
    dispatcher.register("panel.selectAll", {
      available: context => sourceAndTarget(context).source.directoryLocation === null
        ? { enabled: false, reason: "Каталог ещё не открыт." } : { enabled: true },
      execute: request => {
        const panel = request.context.activePanel === "left" ? left.current : right.current;
        if (!panel) throw new CommandError("unavailable", "Панель недоступна.");
        panel.selectAll();
      },
    });
    return dispatcher;
  });

  function activate(id: PanelId) {
    // A button click may follow activation before React commits a new render.
    activeRef.current = id;
    setActive(id);
  }

  function dispatch(id: CommandId) {
    setCommandError("");
    void commands.dispatch(id).catch(error => setCommandError(commandErrorMessage(error)));
  }
  return (
    <AgGridProvider modules={[AllCommunityModule]}>
      <CssBaseline />
      <Box sx={{ height: "100vh", display: "flex", flexDirection: "column", p: 1, gap: 1 }}>
        {commandError && <Alert severity="error" onClose={() => setCommandError("")}>{commandError}</Alert>}
        <Box sx={{ display: "flex", flex: 1, minHeight: 0, gap: 1 }}>
          <FilePanel ref={left} label="Левая панель" active={active === "left"} activate={() => activate("left")} dispatch={dispatch} />
          <FilePanel ref={right} label="Правая панель" active={active === "right"} activate={() => activate("right")} dispatch={dispatch} />
        </Box>
      </Box>
    </AgGridProvider>
  );
}
