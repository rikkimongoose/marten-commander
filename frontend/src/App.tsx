import { useCallback, useRef, useState } from "react";
import { Alert, Box, CssBaseline } from "@mui/material";
import { AgGridProvider } from "ag-grid-react";
import { AllCommunityModule } from "ag-grid-community";
import { FilePanel, type FilePanelHandle } from "./FilePanel";
import { CommandDispatcher, CommandError, commandErrorMessage, sourceAndTarget, type CommandId, type JsonObject, type PanelId } from "./commands";
import { registerHomeCommand, registerLocationCommand, registerPathCommand } from "./locationCommand";
import { registerNavigationCommands } from "./navigationCommands";
import { registerFileCommands } from "./fileCommands";
import { FunctionBar } from "./FunctionBar";
import { CommandLine } from "./CommandLine";
import { GetHomeDirectory } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/volumeservice";

export default function App() {
  const [active, setActive] = useState<PanelId>("left");
  const activeRef = useRef<PanelId>("left");
  const left = useRef<FilePanelHandle>(null);
  const right = useRef<FilePanelHandle>(null);
  const [commandError, setCommandError] = useState("");
  const [, setCommandContextVersion] = useState(0);
  const commandContextChanged = useCallback(() => setCommandContextVersion(version => version + 1), []);
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
    registerLocationCommand(dispatcher, (id, location, volumeId, home) => {
      const panel = id === "left" ? left.current : right.current;
      if (!panel) throw new CommandError("unavailable", "Панель недоступна.");
      panel.navigate(location, volumeId, home);
    });
    const panel = (id: PanelId) => {
      const handle = id === "left" ? left.current : right.current;
      if (!handle) throw new CommandError("unavailable", "Панель недоступна.");
      return handle;
    };
    registerNavigationCommands(dispatcher, {
      openEntry: (id, directory, rowId) => panel(id).openEntry(directory, rowId),
      parent: (id, directory) => panel(id).parent(directory),
      switchPanel: id => { activate(id); panel(id).focusList(); },
    });
    registerPathCommand(dispatcher, (id, path, baseLocation) => panel(id).navigatePath(path, baseLocation));
    registerHomeCommand(dispatcher, GetHomeDirectory, (id, location) => panel(id).navigate(location, undefined, true));
    registerFileCommands(dispatcher);
    return dispatcher;
  });

  function activate(id: PanelId) {
    // A button click may follow activation before React commits a new render.
    activeRef.current = id;
    setActive(id);
  }

  function dispatch(id: CommandId, params?: JsonObject) {
    setCommandError("");
    void commands.dispatch(id, params).catch(error => setCommandError(commandErrorMessage(error)));
  }
  const activePath = (active === "left" ? left.current : right.current)?.getDisplayPath() ?? "";
  return (
    <AgGridProvider modules={[AllCommunityModule]}>
      <CssBaseline />
      <Box sx={{ height: "100vh", display: "flex", flexDirection: "column", p: 1, gap: 1 }}>
        {commandError && <Alert severity="error" onClose={() => setCommandError("")}>{commandError}</Alert>}
        <Box sx={{ display: "flex", flex: 1, minHeight: 0, gap: 1 }}>
          <FilePanel ref={left} label="Левая панель" active={active === "left"} activate={() => activate("left")}
            onContextChange={commandContextChanged} dispatch={dispatch} />
          <FilePanel ref={right} label="Правая панель" active={active === "right"} activate={() => activate("right")}
            onContextChange={commandContextChanged} dispatch={dispatch} />
        </Box>
        <CommandLine path={activePath} dispatch={dispatch} report={setCommandError} />
        <FunctionBar availability={id => commands.availability(id)} dispatch={dispatch} />
      </Box>
    </AgGridProvider>
  );
}
