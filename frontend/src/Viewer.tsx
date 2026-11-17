import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Alert, Box, Button, CircularProgress, Divider, FormControl, Menu, MenuItem, Paper, Select, Typography } from "@mui/material";
import type { CancellablePromise } from "@wailsio/runtime";
import type { ViewerChunk, ViewerFile, ViewerTextChunk } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/models";
import { CloseFile, ReadFile, ReadText } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/viewerservice";
import { commandErrorMessage } from "./commands";
import { decodeBase64, hexPreview, normalizeViewerMode, viewerEscapeAction, viewerModes, type ViewerMode } from "./viewerModes";
import { byteOffsetForScroll, encodingStatus, needsTextWindow, RequestGeneration, textEncodings, textWindowSize,
  virtualWindowLayout, type TextEncoding } from "./textViewer";

const previewSize = 64 * 1024;

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
  const [bytes, setBytes] = useState<Uint8Array>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [menu, setMenu] = useState<{ name: "file" | "edit" | "view"; anchor: HTMLElement }>();
  const request = useRef<ActiveRequest>();
  const generation = useRef(new RequestGeneration());
  const root = useRef<HTMLDivElement>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const position = useRef("0");
  const closing = useRef(false);
  const scrollFrame = useRef(0);
  const fileSize = useMemo(() => BigInt(file.size), [file.size]);

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
        position.current = chunk.startOffset;
        setTextChunk(chunk);
        setLoading(false);
      }
    }).catch(reason => fail(reason, token));
  }, [fail, file.id]);

  const loadHex = useCallback(() => {
    const token = generation.current.begin();
    void request.current?.cancel("viewer mode changed");
    setLoading(true); setError("");
    const next = ReadFile(file.id, "0", previewSize);
    request.current = next;
    next.then(chunk => {
      if (!closing.current && generation.current.current(token)) {
        setBytes(decodeBase64(chunk.dataBase64)); setLoading(false);
      }
    }).catch(reason => fail(reason, token));
  }, [fail, file.id]);

  const closeViewer = useCallback(() => {
    if (closing.current) return;
    closing.current = true;
    generation.current.invalidate();
    cancelAnimationFrame(scrollFrame.current);
    void request.current?.cancel("viewer closed");
    void CloseFile(file.id).catch(reason => reportCloseError(commandErrorMessage(reason))).finally(close);
  }, [close, file.id, reportCloseError]);

  useEffect(() => {
    root.current?.focus();
    if (mode === "text") loadText(position.current, encoding);
    else if (mode === "hex") loadHex();
  }, [encoding, loadHex, loadText, mode]);

  useEffect(() => () => {
    generation.current.invalidate();
    cancelAnimationFrame(scrollFrame.current);
    void request.current?.cancel("viewer unmounted");
  }, []);

  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      const action = viewerEscapeAction(event.key, !!menu);
      if (action === "none") return;
      event.preventDefault();
      if (action === "closeMenu") setMenu(undefined);
      else closeViewer();
    };
    window.addEventListener("keydown", keydown);
    return () => window.removeEventListener("keydown", keydown);
  }, [closeViewer, menu]);

  const openMenu = (name: "file" | "edit" | "view") => (event: React.MouseEvent<HTMLElement>) =>
    setMenu({ name, anchor: event.currentTarget });
  const chooseMode = (next: ViewerMode) => { setMode(next); setMenu(undefined); };

  const layout = useMemo(() => textChunk ? virtualWindowLayout(fileSize, BigInt(textChunk.startOffset),
    BigInt(textChunk.nextOffset), textChunk.text, scroller.current?.clientHeight ?? 600) : undefined,
  [fileSize, textChunk]);

  const onTextScroll = () => {
    cancelAnimationFrame(scrollFrame.current);
    scrollFrame.current = requestAnimationFrame(() => {
      const element = scroller.current;
      if (!element || !textChunk) return;
      const target = byteOffsetForScroll(element.scrollTop, element.scrollHeight, element.clientHeight, fileSize);
      if (needsTextWindow(target, BigInt(textChunk.startOffset), BigInt(textChunk.nextOffset))) loadText(target.toString(), encoding);
    });
  };

  const content = () => {
    if (error) return <Alert severity="error">{error}</Alert>;
    if (mode === "image") return <Typography>Режим изображений будет реализован в Issue #23.</Typography>;
    if (mode === "unknown") return <Typography>Формат файла не поддерживается. Выберите текстовый или HEX-режим.</Typography>;
    if (mode === "hex") {
      if (!bytes) return <CircularProgress aria-label="Чтение файла" />;
      return <Box component="pre" aria-label="HEX файла" sx={{ m: 0, minWidth: "100%", width: "max-content", whiteSpace: "pre",
        fontFamily: "monospace", fontSize: 13 }}>{hexPreview(bytes)}</Box>;
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
      {menu?.name === "view" && viewerModes.map(item => <MenuItem key={item.id} selected={mode === item.id}
        disabled={!item.available} title={item.note} onClick={() => chooseMode(item.id)}>{item.label}{item.note && " — не готово"}</MenuItem>)}
    </Menu>
    <Divider />
    <Box sx={{ px: 2, py: 1, bgcolor: "background.paper" }}>
      <Typography variant="body2" sx={{ fontFamily: "monospace", overflowWrap: "anywhere" }}>{file.path}</Typography>
      <Typography variant="caption" color="text.secondary">
        {fileSize.toLocaleString("ru-RU")} байт · только чтение{mode === "text" ? ` · ${status}` : ` · показаны первые ${previewSize.toLocaleString("ru-RU")} байт`}
      </Typography>
    </Box>
    <Divider />
    <Box ref={scroller} onScroll={mode === "text" ? onTextScroll : undefined} sx={{ flex: 1, minHeight: 0, overflow: "auto", p: 2 }}>
      {content()}
    </Box>
  </Paper>;
}
