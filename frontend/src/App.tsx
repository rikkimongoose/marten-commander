import { prepareTrash, registerTrashCommand, type TrashPreview } from "./trashCommand";
import { TrashDialog } from "./TrashDialog";
import { registerTerminalCommand } from "./terminalCommand";
import { useCallback, useRef, useState } from "react";
import { Alert, Box, CssBaseline } from "@mui/material";
import { AgGridProvider } from "ag-grid-react";
import { AllCommunityModule } from "ag-grid-community";
import { FilePanel, type FilePanelHandle } from "./FilePanel";
import { CANCEL_COMMAND, CommandDispatcher, CommandError, commandErrorMessage, effectiveSelection, sourceAndTarget,
  type CommandContext, type CommandId, type JsonObject, type PanelId } from "./commands";
import { registerHomeCommand, registerLocationCommand, registerPathCommand } from "./locationCommand";
import { registerNavigationCommands } from "./navigationCommands";
import { moveDestinationError, registerFileCommands } from "./fileCommands";
import { FunctionBar } from "./FunctionBar";
import { CommandLine } from "./CommandLine";
import { GetHomeDirectory } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/volumeservice";
import { MkdirDialog } from "./MkdirDialog";
import type { CopyPromptResult, MkdirPromptResult, MovePromptResult } from "./fileCommands";
import { OperationsCenter } from "./OperationsCenter";
import { CopyDialog } from "./CopyDialog";
import type { Operation } from "../bindings/github.com/rikkimongoose/marten-commander/internal/operations/models";
import { moveFocusCandidates, operationRefreshLocations } from "./operationRefresh";

type MkdirDialogState = { open: boolean; destination: string; busy: boolean; error: string };
type CopyDialogState = { open: boolean; sources: string[]; destination: string; busy: boolean; error: string };

export default function App() {
  const [active, setActive] = useState<PanelId>("left");
  const activeRef = useRef<PanelId>("left");
  const left = useRef<FilePanelHandle>(null);
  const right = useRef<FilePanelHandle>(null);
  const [commandError, setCommandError] = useState("");
  const [mkdirDialog, setMkdirDialog] = useState<MkdirDialogState>({ open: false, destination: "", busy: false, error: "" });
  const mkdirResolve = useRef<((result: MkdirPromptResult) => void)>();
  const [copyDialog, setCopyDialog] = useState<CopyDialogState>({ open: false, sources: [], destination: "", busy: false, error: "" });
  const copyResolve = useRef<((result: CopyPromptResult) => void)>();
  const [moveDialog, setMoveDialog] = useState<CopyDialogState>({ open: false, sources: [], destination: "", busy: false, error: "" });
  const moveResolve = useRef<((result: MovePromptResult) => void)>();
  const [trashPreview, setTrashPreview] = useState<TrashPreview | null>(null);
  const trashResolve = useRef<((confirmed: boolean) => void)>();
  const [, setCommandContextVersion] = useState(0);
  const commandContextChanged = useCallback(() => setCommandContextVersion(version => version + 1), []);
  const [commands] = useState(() => {
    const empty = () => ({ directoryLocation: null, selectedLocations: [], focusedLocation: null, focusedKind: null });
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
    registerTerminalCommand(dispatcher);
    registerTrashCommand(dispatcher, {
      preview: prepareTrash,
      confirm: preview => { setTrashPreview(preview); return new Promise(resolve => { trashResolve.current = resolve; }); },
    });
    registerFileCommands(dispatcher, undefined, {
      prompt: { open: openMkdirDialog, retry: retryMkdirDialog, complete: closeMkdirDialog },
      succeeded: (request, name) => {
        const directory = sourceAndTarget(request.context).source.directoryLocation;
        if (directory) panel(request.context.activePanel).refreshCreated(directory, name);
      },
    }, {
      prompt: { open: openCopyDialog, retry: retryCopyDialog, complete: closeCopyDialog },
    }, {
      prompt: { open: openMoveDialog, retry: retryMoveDialog, complete: closeMoveDialog },
    });
    return dispatcher;
  });

  function finishTrash(confirmed: boolean) {
    const resolve = trashResolve.current;
    trashResolve.current = undefined;
    setTrashPreview(null);
    resolve?.(confirmed);
  }

  function activate(id: PanelId) {
    // A button click may follow activation before React commits a new render.
    activeRef.current = id;
    setActive(id);
  }

  function dispatch(id: CommandId, params?: JsonObject) {
    setCommandError("");
    void commands.dispatch(id, params).catch(error => setCommandError(commandErrorMessage(error)));
  }
  function focusActivePanel() {
    (activeRef.current === "left" ? left.current : right.current)?.focusList();
  }
  function openMkdirDialog(context: CommandContext): Promise<MkdirPromptResult> {
    const handle = context.activePanel === "left" ? left.current : right.current;
    setMkdirDialog({ open: true, destination: handle?.getDisplayPath() ?? "", busy: false, error: "" });
    return new Promise(resolve => { mkdirResolve.current = resolve; });
  }
  function retryMkdirDialog(message: string): Promise<MkdirPromptResult> {
    setMkdirDialog(value => ({ ...value, busy: false, error: message }));
    return new Promise(resolve => { mkdirResolve.current = resolve; });
  }
  function closeMkdirDialog() {
    mkdirResolve.current = undefined;
    setMkdirDialog(value => ({ ...value, open: false, busy: false, error: "" }));
  }
  function submitMkdir(name: string) {
    const resolve = mkdirResolve.current;
    if (!resolve || mkdirDialog.busy) return;
    mkdirResolve.current = undefined;
    setMkdirDialog(value => ({ ...value, busy: true, error: "" }));
    resolve({ name });
  }
  function cancelMkdir() {
    const resolve = mkdirResolve.current;
    if (mkdirDialog.busy) return;
    mkdirResolve.current = undefined;
    setMkdirDialog(value => ({ ...value, open: false, error: "" }));
    resolve?.(CANCEL_COMMAND);
  }
  function openCopyDialog(context: CommandContext): Promise<CopyPromptResult> {
    const { source } = sourceAndTarget(context);
    const sourceHandle = context.activePanel === "left" ? left.current : right.current;
    const targetHandle = context.activePanel === "left" ? right.current : left.current;
    const locations = effectiveSelection(source);
    setCopyDialog({ open: true, sources: sourceHandle?.describeLocations(locations) ?? [...locations],
      destination: targetHandle?.getDisplayPath() ?? "", busy: false, error: "" });
    return new Promise(resolve => { copyResolve.current = resolve; });
  }
  function retryCopyDialog(message: string): Promise<CopyPromptResult> {
    setCopyDialog(value => ({ ...value, busy: false, error: message }));
    return new Promise(resolve => { copyResolve.current = resolve; });
  }
  function closeCopyDialog() {
    copyResolve.current = undefined;
    setCopyDialog(value => ({ ...value, open: false, busy: false, error: "" }));
  }
  function submitCopy(destination: string) {
    const resolve = copyResolve.current;
    if (!resolve || copyDialog.busy) return;
    copyResolve.current = undefined;
    setCopyDialog(value => ({ ...value, destination, busy: true, error: "" }));
    resolve({ destination });
  }
  function cancelCopy() {
    const resolve = copyResolve.current;
    if (copyDialog.busy) return;
    copyResolve.current = undefined;
    setCopyDialog(value => ({ ...value, open: false, error: "" }));
    resolve?.(CANCEL_COMMAND);
  }
  function openMoveDialog(context: CommandContext): Promise<MovePromptResult> {
    const { source } = sourceAndTarget(context);
    const sourceHandle = context.activePanel === "left" ? left.current : right.current;
    const targetHandle = context.activePanel === "left" ? right.current : left.current;
    const locations = effectiveSelection(source);
    setMoveDialog({ open: true, sources: sourceHandle?.describeLocations(locations) ?? [...locations],
      destination: targetHandle?.getDisplayPath() ?? "", busy: false, error: "" });
    return new Promise(resolve => { moveResolve.current = resolve; });
  }
  function retryMoveDialog(message: string): Promise<MovePromptResult> {
    setMoveDialog(value => ({ ...value, busy: false, error: message }));
    return new Promise(resolve => { moveResolve.current = resolve; });
  }
  function closeMoveDialog() {
    moveResolve.current = undefined;
    setMoveDialog(value => ({ ...value, open: false, busy: false, error: "" }));
  }
  function submitMove(destination: string) {
    const resolve = moveResolve.current;
    if (!resolve || moveDialog.busy) return;
    moveResolve.current = undefined;
    setMoveDialog(value => ({ ...value, destination, busy: true, error: "" }));
    resolve({ destination });
  }
  function cancelMove() {
    const resolve = moveResolve.current;
    if (moveDialog.busy) return;
    moveResolve.current = undefined;
    setMoveDialog(value => ({ ...value, open: false, error: "" }));
    resolve?.(CANCEL_COMMAND);
  }
  function refreshOperationPanels(operation: Operation) {
    // Refresh any panel that still displays the fixed destination. Navigation
    // performed after launch is never undone, and an edited destination may
    // currently be visible in either panel.
    for (const location of operationRefreshLocations(operation)) {
      left.current?.refreshDirectory(location);
      right.current?.refreshDirectory(location);
    }
    for (const candidate of moveFocusCandidates(operation)) {
      left.current?.refreshCreated(candidate.directoryLocation, candidate.name);
      right.current?.refreshCreated(candidate.directoryLocation, candidate.name);
    }
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
        <Box sx={{ display: "flex", alignItems: "stretch", gap: 1, minWidth: 0 }}>
          <Box sx={{ flex: 1, minWidth: 0 }}>
            <CommandLine path={activePath} dispatch={dispatch} report={setCommandError} returnToPanel={focusActivePanel} />
          </Box>
          <OperationsCenter onOperationFinished={refreshOperationPanels} />
        </Box>
        <FunctionBar availability={id => commands.availability(id)} dispatch={dispatch} />
        <TrashDialog preview={trashPreview} confirm={() => finishTrash(true)} cancel={() => finishTrash(false)} closed={focusActivePanel} />
        <MkdirDialog {...mkdirDialog} submit={submitMkdir} cancel={cancelMkdir} closed={focusActivePanel} />
        <CopyDialog open={copyDialog.open} sources={copyDialog.sources} initialDestination={copyDialog.destination}
          busy={copyDialog.busy} error={copyDialog.error} submit={submitCopy} cancel={cancelCopy} closed={focusActivePanel} />
        <CopyDialog open={moveDialog.open} sources={moveDialog.sources} initialDestination={moveDialog.destination}
          busy={moveDialog.busy} error={moveDialog.error} submit={submitMove} cancel={cancelMove} closed={focusActivePanel}
          title="Перемещение файлов" destinationLabel="Новое имя, каталог или полный путь" confirmLabel="Переместить"
          destinationHelp={moveDialog.sources.length === 1
            ? "Новое имя переименует объект на месте. Существующий каталог переместит его без переименования; полный новый путь выполнит оба действия."
            : "Для нескольких объектов укажите абсолютный путь существующего каталога."}
          validateDestination={destination => moveDestinationError(destination, moveDialog.sources.length)} />
      </Box>
    </AgGridProvider>
  );
}
