import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Alert, Box, Button, CircularProgress, Dialog, DialogActions, DialogContent, Divider, FormControl,
  Menu, MenuItem, Paper, Select, TextField, Typography } from "@mui/material";
import type { CancellablePromise } from "@wailsio/runtime";
import type { ViewerChunk, ViewerFile, ViewerTextChunk } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/models";
import { CloseFile, ReadFile, ReadText } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/viewerservice";
import { commandErrorMessage } from "./commands";
import { decodeBase64, hexPreviewRows, normalizeViewerMode, viewerEscapeAction, viewerModes, type ViewerMode } from "./viewerModes";
import { HexLoadScheduler, hexOffsetForScroll, hexScrollTopForOffset, hexVirtualLayout, hexVisibleWindow, hexWindowForOffset, needsHexWindow,
  parseHexOffset } from "./hexViewer";
import { byteOffsetForScroll, encodingStatus, needsTextWindow, RequestGeneration, textEncodings, textWindowSize,
  virtualWindowLayout, type TextEncoding } from "./textViewer";
import { ImageViewer } from "./ImageViewer";

interface ViewerProps {
  file: ViewerFile;
  close: () => void;
  reportCloseError: (message: string) => void;
}

type ActiveRequest = CancellablePromise<ViewerChunk> | CancellablePromise<ViewerTextChunk>;

export function Viewer({ file, close, reportCloseError }: ViewerProps) {
  const [mode, setMode] = useState<ViewerMode>(() => normalizeViewerMode(file.initialMode));
  const [encoding, setEncoding] = useState<TextEncoding>("auto");
  const [textChunk, setTextChunk] = useState<ViewerTextChunk>();
  const [hexChunk, setHexChunk] = useState<{ start: bigint; bytes: Uint8Array; eof: boolean }>();
  const [selectedHexOffset, setSelectedHexOffset] = useState<bigint>();
  const [offsetDialog, setOffsetDialog] = useState({ open: false, value: "", error: "" });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [menu, setMenu] = useState<{ name: "file" | "edit" | "view"; anchor: HTMLElement }>();
  const request = useRef<ActiveRequest>();
  const generation = useRef(new RequestGeneration());
  const root = useRef<HTMLDivElement>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const textPosition = useRef("0");
  const hexPosition = useRef(0n);
  const scrollAfterHexLoad = useRef<bigint>();
  const closing = useRef(false);
  const scrollFrame = useRef(0);
  const hexLoader = useRef<(offset: bigint) => void>(() => {});
  const hexScheduler = useRef<HexLoadScheduler>();
  if (!hexScheduler.current) hexScheduler.current = new HexLoadScheduler(offset => hexLoader.current(offset));
  const fileSize = useMemo(() => BigInt(file.size), [file.size]);
  const [hexSize, setHexSize] = useState(fileSize);
  const [hexViewportOffset, setHexViewportOffset] = useState(0n);
  const hexSizeRef = useRef(fileSize);

  const fail = useCallback((reason: unknown, token: number) => {
    if (!closing.current && generation.current.current(token) && (!(reason instanceof Error) || reason.name !== "CancelError")) {
      setError(commandErrorMessage(reason));
      setLoading(false);
    }
  }, []);

  const loadText = useCallback((offset: string, selected: TextEncoding) => {
    const token = generation.current.begin();
    void request.current?.cancel("superseded text window");
    setLoading(true); setError("");
    const next = ReadText(file.id, offset, textWindowSize, selected);
    request.current = next;
    next.then(chunk => {
      if (!closing.current && generation.current.current(token)) {
        textPosition.current = chunk.startOffset;
        setTextChunk(chunk);
        setLoading(false);
      }
    }).catch(reason => fail(reason, token));
  }, [fail, file.id]);

  const loadHex = useCallback((offset: bigint) => {
    hexScheduler.current?.cancel();
    const window = hexWindowForOffset(offset, hexSizeRef.current);
    hexPosition.current = offset;
    const token = generation.current.begin();
    void request.current?.cancel("superseded HEX window");
    setError("");
    if (window.length === 0) {
      setHexChunk({ start: 0n, bytes: new Uint8Array(), eof: true });
      setLoading(false);
      return;
    }
    setLoading(true);
    const next = ReadFile(file.id, window.start.toString(), window.length);
    request.current = next;
    next.then(chunk => {
      if (!closing.current && generation.current.current(token)) {
        const start = BigInt(chunk.offset);
        const decoded = decodeBase64(chunk.dataBase64);
        const currentSize = BigInt(chunk.size);
        hexSizeRef.current = currentSize;
        setHexSize(currentSize);
        setHexChunk({ start, bytes: decoded, eof: chunk.eof });
        setHexViewportOffset(offset);
        setLoading(false);
      }
    }).catch(reason => fail(reason, token));
  }, [fail, file.id]);
  hexLoader.current = loadHex;

  const closeViewer = useCallback(() => {
    if (closing.current) return;
    closing.current = true;
    generation.current.invalidate();
    cancelAnimationFrame(scrollFrame.current);
    hexScheduler.current?.cancel();
    void request.current?.cancel("viewer closed");
    void CloseFile(file.id).catch(reason => reportCloseError(commandErrorMessage(reason))).finally(close);
  }, [close, file.id, reportCloseError]);

  useEffect(() => {
    root.current?.focus();
    if (mode === "text") loadText(textPosition.current, encoding);
    else if (mode === "hex") loadHex(hexPosition.current);
    else {
      generation.current.invalidate();
      void request.current?.cancel("viewer mode changed");
      setLoading(false);
      setError("");
    }
  }, [encoding, loadHex, loadText, mode]);

  useEffect(() => () => {
    generation.current.invalidate();
    cancelAnimationFrame(scrollFrame.current);
    hexScheduler.current?.cancel();
    void request.current?.cancel("viewer unmounted");
  }, []);

  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && offsetDialog.open) return;
      const action = viewerEscapeAction(event.key, !!menu);
      if (action === "none") return;
      event.preventDefault();
      if (action === "closeMenu") setMenu(undefined);
      else closeViewer();
    };
    window.addEventListener("keydown", keydown);
    return () => window.removeEventListener("keydown", keydown);
  }, [closeViewer, menu, offsetDialog.open]);

  const openMenu = (name: "file" | "edit" | "view") => (event: React.MouseEvent<HTMLElement>) =>
    setMenu({ name, anchor: event.currentTarget });
  const chooseMode = (next: ViewerMode) => { setMode(next); setMenu(undefined); };

  const layout = useMemo(() => textChunk ? virtualWindowLayout(fileSize, BigInt(textChunk.startOffset),
    BigInt(textChunk.nextOffset), textChunk.text, scroller.current?.clientHeight ?? 600) : undefined,
  [fileSize, textChunk]);
  const visibleHex = useMemo(() => hexChunk ? hexVisibleWindow(hexChunk.bytes, hexChunk.start, hexViewportOffset,
    scroller.current?.clientHeight ?? 600) : undefined, [hexChunk, hexViewportOffset]);
  const hexLayout = useMemo(() => visibleHex ? hexVirtualLayout(hexSize, visibleHex.start, visibleHex.bytes.length,
    scroller.current?.clientHeight ?? 600) : undefined, [hexSize, visibleHex]);

  useEffect(() => {
    const offset = scrollAfterHexLoad.current;
    const element = scroller.current;
    if (mode !== "hex" || offset === undefined || !element || !hexChunk) return;
    const frame = requestAnimationFrame(() => {
      element.scrollTop = hexScrollTopForOffset(offset, element.scrollHeight, element.clientHeight, hexSize);
      scrollAfterHexLoad.current = undefined;
    });
    return () => cancelAnimationFrame(frame);
  }, [hexChunk, hexSize, mode]);

  const onTextScroll = () => {
    cancelAnimationFrame(scrollFrame.current);
    scrollFrame.current = requestAnimationFrame(() => {
      const element = scroller.current;
      if (!element || !textChunk) return;
      const target = byteOffsetForScroll(element.scrollTop, element.scrollHeight, element.clientHeight, fileSize);
      if (needsTextWindow(target, BigInt(textChunk.startOffset), BigInt(textChunk.nextOffset))) loadText(target.toString(), encoding);
    });
  };

  const onHexScroll = () => {
    cancelAnimationFrame(scrollFrame.current);
    scrollFrame.current = requestAnimationFrame(() => {
      const element = scroller.current;
      if (!element || !hexChunk) return;
      const target = hexOffsetForScroll(element.scrollTop, element.scrollHeight, element.clientHeight, hexSize);
      hexPosition.current = target;
      setHexViewportOffset(target);
      if (needsHexWindow(target, hexChunk.start, hexChunk.bytes.length, hexSize)) {
        hexScheduler.current?.schedule(target);
      }
    });
  };

  const jumpToOffset = () => {
    try {
      const offset = parseHexOffset(offsetDialog.value, hexSize);
      setOffsetDialog({ open: false, value: "", error: "" });
      setSelectedHexOffset(offset);
      setHexViewportOffset(offset);
      scrollAfterHexLoad.current = offset;
      loadHex(offset);
    } catch (reason) {
      setOffsetDialog(value => ({ ...value, error: reason instanceof Error ? reason.message : String(reason) }));
    }
  };

  const content = () => {
    if (error) return <Alert severity="error">{error}</Alert>;
    if (mode === "image") return <ImageViewer fileID={file.id} name={file.name} mediaType={file.mediaType} viewport={scroller} />;
    if (mode === "unknown") return <Typography>Формат файла не поддерживается. Выберите текстовый или HEX-режим.</Typography>;
    if (mode === "hex") {
      if (!hexChunk) return <CircularProgress aria-label="Чтение файла" />;
      if (hexSize === 0n) return <Typography color="text.secondary">Файл пуст.</Typography>;
      return <Box aria-label="HEX файла" sx={{ minWidth: "100%", width: "max-content", fontFamily: "monospace", fontSize: 13 }}>
        <Box sx={{ height: hexLayout?.topHeight ?? 0 }} />
        {hexPreviewRows(visibleHex?.bytes ?? new Uint8Array(), visibleHex?.start ?? 0n).map(row => <Box key={row.offset.toString()} data-hex-row
          sx={{ display: "flex", height: "18px", lineHeight: "18px" }}>
          <Box component="span" sx={{ mr: 2 }}>{row.address}</Box>
          <Box component="span" sx={{ display: "flex", mr: 2 }}>
            {Array.from({ length: 16 }, (_, index) => {
              const absolute = row.offset + BigInt(index);
              const selected = absolute === selectedHexOffset;
              return <Box component="span" key={index} sx={{ width: "3ch", ml: index === 8 ? "1ch" : 0,
                bgcolor: selected ? "primary.main" : undefined, color: selected ? "primary.contrastText" : undefined }}>
                {row.values[index] === undefined ? "  " : row.values[index].toString(16).padStart(2, "0")}
              </Box>;
            })}
          </Box>
          <Box component="span">|{row.ascii.map((character, index) => {
            const selected = row.offset + BigInt(index) === selectedHexOffset;
            return <Box component="span" key={index} sx={{ bgcolor: selected ? "primary.main" : undefined,
              color: selected ? "primary.contrastText" : undefined }}>{character}</Box>;
          })}|</Box>
        </Box>)}
        <Box sx={{ height: hexLayout?.bottomHeight ?? 0 }} />
      </Box>;
    }
    if (!textChunk) return <CircularProgress aria-label="Чтение файла" />;
    if (fileSize === 0n) return <Typography color="text.secondary">Файл пуст.</Typography>;
    return <Box sx={{ minWidth: "100%", width: "max-content" }}>
      <Box sx={{ height: layout?.topHeight ?? 0 }} />
      <Box component="pre" aria-label="Текст файла" sx={{ m: 0, whiteSpace: "pre", fontFamily: "monospace", fontSize: 13,
        lineHeight: "18px", minHeight: layout?.contentHeight }}>{textChunk.text}</Box>
      <Box sx={{ height: layout?.bottomHeight ?? 0 }} />
    </Box>;
  };

  const status = textChunk ? encodingStatus(textChunk.auto, textChunk.encoding, textChunk.encodingSource)
    : encodingStatus(encoding === "auto", file.encoding, encoding === "auto" ? file.encodingSource : "manual");

  return <Paper ref={root} tabIndex={-1} data-viewer square role="region" aria-label="Просмотр файла" sx={{
    position: "absolute", inset: 0, zIndex: 20, display: "flex", flexDirection: "column", bgcolor: "background.default",
  }}>
    <Box sx={{ display: "flex", alignItems: "center", minHeight: 40, px: 1, gap: .5, bgcolor: "background.paper" }}>
      <Button onClick={openMenu("file")}>Файл</Button>
      <Button onClick={openMenu("edit")}>Правка</Button>
      <Button onClick={openMenu("view")}>Вид</Button>
      {mode === "text" && <FormControl size="small" sx={{ ml: 1, minWidth: 160 }}>
        <Select aria-label="Кодировка" value={encoding} onChange={event => setEncoding(event.target.value as TextEncoding)}>
          {textEncodings.map(item => <MenuItem key={item.id} value={item.id}>{item.label}</MenuItem>)}
        </Select>
      </FormControl>}
      <Typography sx={{ ml: 1, fontWeight: 700, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{file.name}</Typography>
      {loading && <CircularProgress size={18} aria-label="Чтение файла" sx={{ ml: "auto" }} />}
    </Box>
    <Menu anchorEl={menu?.anchor} open={!!menu} onClose={() => setMenu(undefined)}>
      {menu?.name === "file" && <MenuItem onClick={closeViewer}>Закрыть <Box component="span" sx={{ ml: 4, opacity: .7 }}>Esc</Box></MenuItem>}
      {menu?.name === "edit" && <MenuItem disabled>Команды редактирования недоступны</MenuItem>}
      {menu?.name === "view" && [
        ...viewerModes.map(item => <MenuItem key={item.id} selected={mode === item.id}
          disabled={!item.available} title={item.note} onClick={() => chooseMode(item.id)}>{item.label}{item.note && " — не готово"}</MenuItem>),
        <Divider key="offset-divider" />,
        <MenuItem key="offset" disabled={mode !== "hex"} onClick={() => { setMenu(undefined); setOffsetDialog({ open: true,
          value: selectedHexOffset?.toString() ?? hexPosition.current.toString(), error: "" }); }}>Перейти к смещению…</MenuItem>,
      ]}
    </Menu>
    <Divider />
    <Box sx={{ px: 2, py: 1, bgcolor: "background.paper" }}>
      <Typography variant="body2" sx={{ fontFamily: "monospace", overflowWrap: "anywhere" }}>{file.path}</Typography>
      <Typography variant="caption" color="text.secondary">
        {fileSize.toLocaleString("ru-RU")} байт · только чтение{mode === "text" ? ` · ${status}`
          : mode === "hex" && hexChunk ? ` · окно ${hexChunk.start.toString(16).toUpperCase().padStart(8, "0")}h`
          : mode === "image" && file.mediaType ? ` · ${file.mediaType}` : ""}
      </Typography>
    </Box>
    <Divider />
    <Box ref={scroller} onScroll={mode === "text" ? onTextScroll : mode === "hex" ? onHexScroll : undefined}
      sx={{ flex: 1, minHeight: 0, overflow: "auto", p: 2 }}>
      {content()}
    </Box>
    <Dialog open={offsetDialog.open} onClose={() => setOffsetDialog(value => ({ ...value, open: false }))}
      slotProps={{ paper: { component: "form", onSubmit: event => { event.preventDefault(); jumpToOffset(); } } }}>
      <DialogContent sx={{ minWidth: { xs: 280, sm: 420 } }}>
        {offsetDialog.error && <Alert severity="error" sx={{ mb: 1 }}>{offsetDialog.error}</Alert>}
        <TextField autoFocus fullWidth label="Смещение (DEC или HEX)" placeholder="4096 или 0x1000"
          value={offsetDialog.value} error={!!offsetDialog.error}
          onChange={event => setOffsetDialog(value => ({ ...value, value: event.target.value, error: "" }))} />
      </DialogContent>
      <DialogActions>
        <Button onClick={() => setOffsetDialog(value => ({ ...value, open: false }))}>Отмена</Button>
        <Button type="submit" variant="contained" disabled={!offsetDialog.value.trim()}>Перейти</Button>
      </DialogActions>
    </Dialog>
  </Paper>;
}
