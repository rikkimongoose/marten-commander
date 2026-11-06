import { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from "react";
import { Alert, Box, Button, Checkbox, FormControlLabel, LinearProgress, TextField, Typography } from "@mui/material";
import { AgGridReact } from "ag-grid-react";
import type { ColDef, GridApi, RowSelectionOptions } from "ag-grid-community";
import { ReadDirectory } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/directoryservice";
import type { Directory, DirectoryRequest, Entry } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/models";
import { DirectoryLoader } from "./directoryLoader";
import { compareEntries, restoredIndex, visibleEntries, type SortField } from "./fileSorting";
import { CommandError, type CommandId, type PanelContext } from "./commands";

const kinds: Record<string, string> = { file: "Файл", directory: "Каталог", symlink: "Ссылка", other: "Спецфайл" };
function column(field: SortField, headerName: string, width?: number): ColDef<Entry> {
  return {
    field, headerName, width, flex: width ? undefined : 1, minWidth: width ? undefined : 130,
    comparator: (_a, _b, a, b, descending) => a.data && b.data ? compareEntries(a.data, b.data, field, descending) : 0,
  };
}
const columns: ColDef<Entry>[] = [
  { ...column("name", "Имя"), initialSort: "asc", tooltipValueGetter: p => p.data?.detail || p.data?.name },
  column("extension", "Расш.", 75),
  { field: "kind", headerName: "Тип", width: 95, sortable: false,
    valueFormatter: p => p.data?.kind === "symlink" && p.data.navigable ? "Ссылка → 📁" : kinds[p.value] || p.value },
  { ...column("size", "Размер", 100), valueFormatter: p => p.value ? BigInt(p.value).toLocaleString("ru-RU") : "—" },
  { ...column("modifiedAt", "Изменён", 170), valueFormatter: p => p.value ? new Date(p.value).toLocaleString("ru-RU") : "" },
];
const defaultColumn: ColDef<Entry> = { sortable: true, resizable: true, cellDataType: false, sortingOrder: ["asc", "desc"] };
const selection: RowSelectionOptions = { mode: "multiRow", checkboxes: false, headerCheckbox: false, enableClickSelection: true };
const rowHeight = 28;
type View = { current?: string; index: number; top?: string; topIndex: number; focus: boolean };

export interface FilePanelHandle {
  getContext(): PanelContext;
  selectAll(): void;
}

type FilePanelProps = { label: string; active: boolean; activate: () => void; dispatch: (id: CommandId) => void };

export const FilePanel = forwardRef<FilePanelHandle, FilePanelProps>(function FilePanel({ label, active, activate, dispatch }, ref) {
  const [directory, setDirectory] = useState<Directory>();
  const [draft, setDraft] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [showHidden, setShowHidden] = useState(false);
  const [selectedCount, setSelectedCount] = useState(0);
  const snapshot = useRef<Directory>();
  const api = useRef<GridApi<Entry>>();
  const root = useRef<HTMLDivElement>(null);
  const gridRoot = useRef<HTMLDivElement>(null);
  const current = useRef<string>();
  const restore = useRef<View>();
  const loader = useRef<DirectoryLoader>();
  const activeRef = useRef(active);
  activeRef.current = active;
  const entries = useMemo(() => visibleEntries(directory?.entries ?? [], showHidden), [directory, showHidden]);

  useImperativeHandle(ref, () => ({
    getContext: () => ({
      directoryLocation: snapshot.current?.location ?? null,
      selectedLocations: api.current?.getSelectedNodes().flatMap(node => node.data ? [node.data.id] : []) ?? [],
      focusedLocation: current.current ? api.current?.getRowNode(current.current)?.data?.id ?? null : null,
    }),
    selectAll: () => {
      if (!api.current || api.current.isDestroyed()) throw new CommandError("unavailable", "Таблица недоступна.");
      api.current.selectAll("filtered");
    },
  }));

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
    if (topIndex !== null) grid.ensureIndexVisible(topIndex, "top");
    if (view.focus && activeRef.current && index !== null &&
      (document.activeElement === document.body || gridRoot.current?.contains(document.activeElement))) {
      grid.setFocusedCell(index, "name");
    }
    setSelectedCount(grid.getSelectedNodes().length);
  }

  useEffect(() => {
    const reader = new DirectoryLoader(ReadDirectory, {
      start: () => { setLoading(true); setError(""); },
      success: next => {
        const sameDirectory = snapshot.current?.location === next.location;
        restore.current = sameDirectory ? capture() : { index: 0, topIndex: 0, focus: !!gridRoot.current?.contains(document.activeElement) };
        if (!sameDirectory) {
          api.current?.deselectAll();
          current.current = undefined;
        }
        snapshot.current = next;
        setDirectory(next);
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

  function navigate(request: DirectoryRequest) { loader.current?.request(request); }
  function refresh() { navigate({ path: "", location: snapshot.current?.location ?? "" }); }
  function parent() {
    if (snapshot.current) navigate({ path: "", location: snapshot.current.parentLocation });
  }
  function open(entry: Entry | undefined) {
    if (entry?.navigable) navigate({ path: "", location: entry.id });
  }

  return (
    <Box ref={root} onFocusCapture={activate} onMouseDownCapture={activate}
      sx={{ flex: 1, minWidth: 0, minHeight: 0, display: "flex", flexDirection: "column",
        border: "2px solid", borderColor: active ? "primary.main" : "divider", bgcolor: "background.paper" }}>
      <Typography title={directory?.displayPath} sx={{ px: 1, py: 0.5, fontSize: 13, fontWeight: 600, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
        {label}: {directory?.displayPath ?? (loading ? "Загрузка каталога…" : "Каталог не открыт")}
      </Typography>
      <Box component="form" sx={{ display: "flex", gap: 0.5, px: 1 }} onSubmit={event => {
        event.preventDefault();
        if (draft !== "") navigate({ path: draft, location: "" });
      }}>
        <TextField size="small" fullWidth value={draft} placeholder="Абсолютный путь…"
          slotProps={{ htmlInput: { "aria-label": `Путь: ${label}` } }} onChange={event => setDraft(event.target.value)} />
        <Button type="submit" size="small" disabled={draft === ""}>Перейти</Button>
      </Box>
      <Box sx={{ display: "flex", alignItems: "center", px: 0.5, gap: 0.5 }}>
        <Button size="small" onClick={parent} disabled={!directory || directory.location === directory.parentLocation}>↑ Вверх</Button>
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
      <Box ref={gridRoot} sx={{ flex: 1, minHeight: 0, "& .directory-row": { fontWeight: 600 } }} onKeyDownCapture={event => {
        const inCell = event.target instanceof Element && !!event.target.closest(".ag-cell");
        if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "a" && inCell) {
          event.preventDefault(); event.stopPropagation(); dispatch("panel.selectAll");
        } else if (event.key === "Enter" && inCell) {
          event.preventDefault(); event.stopPropagation();
          open(current.current ? api.current?.getRowNode(current.current)?.data : undefined);
        } else if (event.key === "Backspace" && inCell) {
          event.preventDefault(); event.stopPropagation(); parent();
        } else if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "r") {
          event.preventDefault(); event.stopPropagation(); refresh();
        }
      }}>
        <AgGridReact<Entry> rowData={entries} columnDefs={columns} defaultColDef={defaultColumn}
          getRowId={params => params.data.id} rowSelection={selection} rowHeight={rowHeight} headerHeight={32}
          animateRows={false} suppressMultiSort suppressScrollOnNewData
          overlayNoRowsTemplate="<span>Нет видимых файлов</span>"
          getRowClass={params => params.data?.navigable ? "directory-row" : undefined}
          onGridReady={event => { api.current = event.api; }}
          onModelUpdated={restoreView} onFirstDataRendered={restoreView}
          onSortChanged={() => { if (!restore.current) restore.current = capture(); }}
          onSelectionChanged={event => setSelectedCount(event.api.getSelectedNodes().length)}
          onCellFocused={event => {
            if (!restore.current && event.rowIndex !== null) current.current = event.api.getDisplayedRowAtIndex(event.rowIndex)?.data?.id;
          }}
          onRowDoubleClicked={event => open(event.data)} />
      </Box>
      <Typography sx={{ px: 1, py: 0.5, fontSize: 12 }}>
        {entries.length} элементов · выделено {selectedCount}{loading ? " · чтение…" : ""}
      </Typography>
    </Box>
  );
});
