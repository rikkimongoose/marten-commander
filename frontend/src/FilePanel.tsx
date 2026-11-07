import { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from "react";
import { Alert, Box, Button, Checkbox, FormControlLabel, LinearProgress, TextField, Typography } from "@mui/material";
import { alpha } from "@mui/material/styles";
import { AgGridReact } from "ag-grid-react";
import type { ColDef, GridApi } from "ag-grid-community";
import { ReadDirectory } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/directoryservice";
import type { Directory, HomeDirectory } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/models";
import { DirectoryLoader } from "./directoryLoader";
import { compareEntries, restoredIndex, visibleEntries, type SortField } from "./fileSorting";
import { CommandError, type CommandId, type JsonObject, type PanelContext } from "./commands";
import { VolumeBar } from "./VolumeBar";
import { GetVolumeInfo } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/volumeservice";
import { createPanelReader, selectedHomeAfterNavigation, type PanelNavigationRequest } from "./panelNavigation";
import { entryDestination, fileLocation, fileSelection, hasParent, panelRows, returnCursor, type PanelRow } from "./panelRows";
import { isTextInput, panelKeyCommand } from "./panelKeyboard";

const kinds: Record<string, string> = { file: "Файл", directory: "Каталог", symlink: "Ссылка", other: "Спецфайл" };
function column(field: SortField, headerName: string, width?: number): ColDef<PanelRow> {
  return {
    field, headerName, width, flex: width ? undefined : 1, minWidth: width ? undefined : 130,
    comparator: (_a, _b, a, b, descending) => a.data && b.data ? compareEntries(a.data, b.data, field, descending) : 0,
  };
}
const columns: ColDef<PanelRow>[] = [
  { ...column("name", "Имя"), initialSort: "asc", tooltipValueGetter: p => p.data?.detail || p.data?.name },
  column("extension", "Расш.", 75),
  { field: "kind", headerName: "Тип", width: 95, sortable: false,
    valueFormatter: p => p.data?.parent ? "" : p.data?.kind === "symlink" && p.data.navigable ? "Ссылка → 📁" : kinds[p.value] || p.value },
  { ...column("size", "Размер", 100), valueFormatter: p => p.value ? BigInt(p.value).toLocaleString("ru-RU") : "—" },
  { ...column("modifiedAt", "Изменён", 170), valueFormatter: p => p.value ? new Date(p.value).toLocaleString("ru-RU") : "" },
];
const defaultColumn: ColDef<PanelRow> = { sortable: true, resizable: true, cellDataType: false, sortingOrder: ["asc", "desc"] };
const rowHeight = 28;
type View = { current?: string; index: number; top?: string; topIndex: number; focus: boolean };

export interface FilePanelHandle {
  getContext(): PanelContext;
  selectAll(): void;
  navigate(location: string, volumeId?: string, home?: boolean): void;
  openEntry(directory: string, rowId: string): void;
  parent(directory: string): void;
  focusList(): void;
}

type FilePanelProps = { label: string; active: boolean; activate: () => void; dispatch: (id: CommandId, params?: JsonObject) => void };

export const FilePanel = forwardRef<FilePanelHandle, FilePanelProps>(function FilePanel({ label, active, activate, dispatch }, ref) {
  const [directory, setDirectory] = useState<Directory>();
  const [selectedHome, setSelectedHome] = useState<HomeDirectory>();
  const [draft, setDraft] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [showHidden, setShowHidden] = useState(false);
  const [selectedCount, setSelectedCount] = useState(0);
  const [focusedRow, setFocusedRow] = useState<string>();
  const snapshot = useRef<Directory>();
  const api = useRef<GridApi<PanelRow>>();
  const root = useRef<HTMLDivElement>(null);
  const gridRoot = useRef<HTMLDivElement>(null);
  const current = useRef<string>();
  const modifiedClickCursor = useRef<{ rowId?: string; selectClicked: boolean }>();
  const ctrlActivatesPanel = useRef(false);
  const restore = useRef<View>();
  const gridRendered = useRef(false);
  const loader = useRef<DirectoryLoader<PanelNavigationRequest>>();
  const activeRef = useRef(active);
  activeRef.current = active;
  const entries = useMemo(() => visibleEntries(directory?.entries ?? [], showHidden), [directory, showHidden]);
  const rows = useMemo(() => panelRows(directory, entries), [directory, entries]);

  // AG Grid does not automatically re-evaluate getRowClass when only the
  // external keyboard-cursor state changes. Redrawing rows updates classes and
  // preserves row nodes, explicit selection, sorting and scroll position.
  useEffect(() => { api.current?.redrawRows(); }, [focusedRow]);

  useImperativeHandle(ref, () => ({
    navigate: (location, volumeId, home) => navigate({ path: "", location, volumeId, home }),
    openEntry: (directory, rowId) => {
      if (snapshot.current?.location !== directory) return;
      const location = entryDestination(snapshot.current, rowId);
      if (location) navigate({ path: "", location });
    },
    parent: directory => {
      if (snapshot.current?.location === directory && hasParent(snapshot.current)) {
        navigate({ path: "", location: snapshot.current.parentLocation });
      }
    },
    focusList,
    getContext: () => ({
      directoryLocation: snapshot.current?.location ?? null,
      selectedLocations: api.current?.getSelectedNodes().flatMap(node => fileLocation(node.data) ? [node.data!.id] : []) ?? [],
      focusedLocation: current.current ? fileLocation(api.current?.getRowNode(current.current)?.data) : null,
    }),
    selectAll: () => {
      if (!api.current || api.current.isDestroyed()) throw new CommandError("unavailable", "Таблица недоступна.");
      api.current.selectAll("filtered");
    },
  }));

  function focusList() {
    const grid = api.current;
    if (!grid || grid.isDestroyed()) { gridRoot.current?.focus(); return; }
    const remembered = current.current ? grid.getRowNode(current.current)?.rowIndex : null;
    const index = remembered ?? (grid.getDisplayedRowCount() ? 0 : null);
    if (index === null) { gridRoot.current?.focus(); return; }
    current.current = grid.getDisplayedRowAtIndex(index)?.data?.id;
    setFocusedRow(current.current);
    grid.ensureIndexVisible(index);
    grid.setFocusedCell(index, "name");
  }

  function capture(): View {
    const grid = api.current;
    const index = current.current ? grid?.getRowNode(current.current)?.rowIndex ?? 0 : 0;
    // getFirstDisplayedRowIndex includes AG Grid's off-screen row buffer.
    // Rows have a fixed height, so use the viewport's actual scroll position.
    const topIndex = Math.floor((grid?.getVerticalPixelRange().top ?? 0) / rowHeight);
    return { current: current.current, index, topIndex, top: grid?.getDisplayedRowAtIndex(topIndex)?.data?.id,
      focus: !!gridRoot.current?.contains(document.activeElement) };
  }

  function restoreView() {
    const grid = api.current;
    const view = restore.current;
    if (!grid || !view) return;
    restore.current = undefined;
    const ids: string[] = [];
    grid.forEachNodeAfterFilterAndSort(node => { if (node.data) ids.push(node.data.id); });
    const index = restoredIndex(ids, view.current, view.index);
    const topIndex = restoredIndex(ids, view.top, view.topIndex);
    current.current = index === null ? undefined : ids[index];
    setFocusedRow(current.current);
    if (topIndex !== null) grid.ensureIndexVisible(topIndex, "top");
    if (view.focus && activeRef.current &&
      (document.activeElement === document.body || gridRoot.current?.contains(document.activeElement))) {
      if (index === null) gridRoot.current?.focus();
      else grid.setFocusedCell(index, "name");
    }
    setSelectedCount(grid.getSelectedNodes().length);
  }

  useEffect(() => {
    const reader = new DirectoryLoader(createPanelReader(ReadDirectory, GetVolumeInfo), {
      start: () => { setLoading(true); setError(""); },
      success: (next, request) => {
        const sameDirectory = snapshot.current?.location === next.location;
        const returned = returnCursor(snapshot.current, next);
        const focus = !!gridRoot.current?.contains(document.activeElement) ||
          (!snapshot.current && activeRef.current && document.activeElement === document.body);
        restore.current = sameDirectory ? capture() : { current: returned, top: returned,
          index: 0, topIndex: 0, focus };
        if (!sameDirectory) {
          api.current?.deselectAll();
          current.current = undefined;
          setFocusedRow(undefined);
        }
        snapshot.current = next;
        setDirectory(next);
        setSelectedHome(previous => selectedHomeAfterNavigation(previous, next, request));
        setLoading(false);
      },
      failure: reason => {
        setError(reason instanceof Error ? reason.message : String(reason));
        setLoading(false);
      },
    });
    loader.current = reader;
    reader.request({ path: "", location: "" });
    return () => { reader.dispose(); loader.current = undefined; };
  }, []);

  function navigate(request: PanelNavigationRequest) { loader.current?.request(request); }
  function refresh() { navigate({ path: "", location: snapshot.current?.location ?? "" }); }
  function parent() {
    if (hasParent(snapshot.current)) dispatch("panel.goToParent");
  }
  function open(entry: PanelRow | undefined) {
    if (entry?.navigable) dispatch("panel.openEntry", { rowId: entry.id });
  }

  return (
    <Box ref={root} role="region" aria-label={label} data-active={active} onFocusCapture={activate} onMouseDownCapture={event => {
      ctrlActivatesPanel.current = !activeRef.current && event.button === 0 && (event.ctrlKey || event.metaKey) &&
        event.target instanceof Element && !!event.target.closest(".ag-cell");
      activate();
    }}
      sx={{ flex: 1, minWidth: 0, minHeight: 0, display: "flex", flexDirection: "column",
        border: "2px solid", borderColor: active ? "primary.main" : "divider", bgcolor: "background.paper" }}>
      <Typography title={directory?.displayPath} sx={{ px: 1, py: 0.5, fontSize: 13, fontWeight: 600, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
        {label}: {directory?.displayPath ?? (loading ? "Загрузка каталога…" : "Каталог не открыт")}
      </Typography>
      <VolumeBar label={label} directory={directory} selectedHome={selectedHome} choose={(location, volumeId, home) => {
        dispatch("panel.goToLocation", home ? { location, home: true } : volumeId === undefined ? { location } : { location, volumeId });
      }} />
      <Box component="form" sx={{ display: "flex", gap: 0.5, px: 1 }} onSubmit={event => {
        event.preventDefault();
        if (draft !== "") navigate({ path: draft, location: "" });
      }}>
        <TextField size="small" fullWidth value={draft} placeholder="Абсолютный путь…"
          slotProps={{ htmlInput: { "aria-label": `Путь: ${label}` } }} onChange={event => setDraft(event.target.value)} />
        <Button type="submit" size="small" disabled={draft === ""}>Перейти</Button>
      </Box>
      <Box sx={{ display: "flex", alignItems: "center", px: 0.5, gap: 0.5 }}>
        <Button size="small" onClick={parent} disabled={!hasParent(directory)}>↑ Вверх</Button>
        <Button size="small" onClick={refresh}>Обновить</Button>
        <Button size="small" disabled={!directory} onClick={() => dispatch("panel.selectAll")}>Выделить всё</Button>
        <FormControlLabel sx={{ ml: "auto", mr: 0 }} control={<Checkbox size="small" checked={showHidden} onChange={event => {
          restore.current = capture();
          setShowHidden(event.target.checked);
        }} />} label={<Typography sx={{ fontSize: 12 }}>Скрытые</Typography>} />
      </Box>
      <Box sx={{ height: 3 }}>{loading && <LinearProgress aria-label={`Чтение: ${label}`} />}</Box>
      {error && <Alert severity="error" sx={{ py: 0, fontSize: 12, overflowWrap: "anywhere" }}>
        {error}{directory && " Показан последний успешно прочитанный каталог."}
      </Alert>}
      <Box ref={gridRoot} tabIndex={0} role="group" aria-label={`Файлы: ${label}`}
        onFocus={event => { if (event.target === gridRoot.current) focusList(); }}
        onMouseDownCapture={event => {
          if (event.button === 0 && (event.ctrlKey || event.metaKey || event.shiftKey) &&
            event.target instanceof Element && event.target.closest(".ag-cell")) {
            // AG Grid focuses the clicked cell before updating its row
            // selection. Preserve our independent keyboard cursor until the
            // grid has finished applying Ctrl/Shift selection.
            modifiedClickCursor.current = {
              // In an inactive panel the clicked row becomes both the current
              // row and an explicitly selected row. In the active panel a
              // modifier-click leaves the current row where it was.
              rowId: ctrlActivatesPanel.current ? undefined : current.current,
              selectClicked: ctrlActivatesPanel.current,
            };
          }
        }}
        sx={theme => ({ flex: 1, minHeight: 0, outlineOffset: -2,
          "--ag-selected-row-background-color": alpha(theme.palette.error.main, 0.16),
          "& .directory-row": { fontWeight: 600 },
          "& .ag-row-selected .ag-cell": { color: "error.main" },
          "& .ag-row.cursor-row": { outline: "2px solid", outlineColor: "primary.main", outlineOffset: -2, zIndex: 1 },
          "& .ag-row.cursor-row:not(.ag-row-selected) .ag-cell": { bgcolor: "action.focus" },
          "& .ag-cell-focus:not(.ag-cell-range-selected):focus-within": { borderColor: "transparent !important" },
        })} onKeyDownCapture={event => {
        if (!(event.target instanceof Element)) return;
        const inCell = !!event.target.closest(".ag-cell");
        const command = panelKeyCommand(event.nativeEvent, isTextInput(event.target));
        if (!command || (!inCell && event.target !== gridRoot.current && command !== "panel.switch")) return;
        event.preventDefault(); event.stopPropagation();
        if (command === "panel.openEntry") open(current.current ? api.current?.getRowNode(current.current)?.data : undefined);
        else if (command === "panel.goToParent") parent();
        else if (command === "refresh") refresh();
        else dispatch(command);
      }}>
        <AgGridReact<PanelRow> rowData={rows} columnDefs={columns} defaultColDef={defaultColumn}
          getRowId={params => params.data.id} rowSelection={fileSelection} rowHeight={rowHeight} headerHeight={32}
          animateRows={false} suppressMultiSort suppressScrollOnNewData
          focusGridInnerElement={() => { focusList(); return true; }}
          navigateToNextCell={params => params.nextCellPosition?.rowIndex === -1 ? params.previousCellPosition : params.nextCellPosition}
          overlayNoRowsTemplate="<span>Нет видимых файлов</span>"
          getRowClass={params => [params.data?.navigable && "directory-row", params.data?.id === focusedRow && "cursor-row"].filter(Boolean).join(" ")}
          onGridReady={event => { api.current = event.api; }}
          onModelUpdated={event => {
            // Initial empty rowData can emit after the first read completes but
            // before React passes its rows to AG Grid. Do not consume that view.
            const emptyRoot = snapshot.current && visibleEntries(snapshot.current.entries ?? [], showHidden).length === 0 &&
              !hasParent(snapshot.current);
            if (gridRendered.current || (emptyRoot && event.api.getDisplayedRowCount() === 0)) restoreView();
          }}
          onFirstDataRendered={() => { gridRendered.current = true; restoreView(); }}
          onSortChanged={() => { if (!restore.current) restore.current = capture(); }}
          onSelectionChanged={event => setSelectedCount(event.api.getSelectedNodes().length)}
          onCellClicked={event => {
            const preserved = modifiedClickCursor.current;
            if (!preserved) return;
            queueMicrotask(() => {
              if (modifiedClickCursor.current !== preserved || event.api.isDestroyed()) return;
              modifiedClickCursor.current = undefined;
              ctrlActivatesPanel.current = false;
              if (preserved.selectClicked && fileLocation(event.data)) {
                event.node.setSelected(true, false, "api");
              }
              const rowId = preserved.rowId ?? event.data?.id;
              const index = rowId ? event.api.getRowNode(rowId)?.rowIndex : null;
              if (!rowId || index === null || index === undefined) return;
              current.current = rowId;
              setFocusedRow(rowId);
              event.api.setFocusedCell(index, "name");
            });
          }}
          onCellFocused={event => {
            if (!restore.current && !modifiedClickCursor.current && event.rowIndex !== null) {
              current.current = event.api.getDisplayedRowAtIndex(event.rowIndex)?.data?.id;
              setFocusedRow(current.current);
            }
          }}
          onRowDoubleClicked={event => open(event.data)} />
      </Box>
      <Typography sx={{ px: 1, py: 0.5, fontSize: 12 }}>
        {entries.length} элементов · выделено {selectedCount}{loading ? " · чтение…" : ""}
      </Typography>
    </Box>
  );
});
