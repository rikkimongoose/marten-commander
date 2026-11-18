import { useEffect, useMemo, useState, type RefObject } from "react";
import { Alert, Box, Button, CircularProgress, Typography } from "@mui/material";
import { adjacentImageZoom, imageAssetURL, imageFitScale, imageZoomLabel } from "./imageViewer";

interface ImageViewerProps {
  fileID: string;
  name: string;
  mediaType: string;
  viewport: RefObject<HTMLDivElement>;
}

interface ImageSize { width: number; height: number }
interface LoadedImage extends ImageSize { source: string }

const checkerboard = {
  backgroundColor: "background.default",
  backgroundImage: "linear-gradient(45deg, rgba(127,127,127,.14) 25%, transparent 25%), linear-gradient(-45deg, rgba(127,127,127,.14) 25%, transparent 25%), linear-gradient(45deg, transparent 75%, rgba(127,127,127,.14) 75%), linear-gradient(-45deg, transparent 75%, rgba(127,127,127,.14) 75%)",
  backgroundSize: "20px 20px",
  backgroundPosition: "0 0, 0 10px, 10px -10px, -10px 0",
} as const;

export function ImageViewer({ fileID, name, mediaType, viewport }: ImageViewerProps) {
  const [loaded, setLoaded] = useState<LoadedImage>();
  const [imageElement, setImageElement] = useState<HTMLImageElement | null>(null);
  const [viewportSize, setViewportSize] = useState<ImageSize>({ width: 0, height: 0 });
  const [fit, setFit] = useState(true);
  const [manualScale, setManualScale] = useState(1);
  const [failedSource, setFailedSource] = useState("");
  const source = useMemo(() => imageAssetURL(fileID, window.location), [fileID]);
  const natural = loaded?.source === source ? loaded : undefined;
  const failed = failedSource === source;

  useEffect(() => {
    const element = viewport.current;
    if (!element) return;
    const update = () => setViewportSize({ width: element.clientWidth, height: element.clientHeight });
    update();
    if (typeof ResizeObserver !== "undefined") {
      const observer = new ResizeObserver(update);
      observer.observe(element);
      return () => observer.disconnect();
    }
    window.addEventListener("resize", update);
    return () => window.removeEventListener("resize", update);
  }, [viewport]);

  useEffect(() => {
    if (!imageElement) return;
    let active = true;
    const ready = () => {
      if (!active) return;
      if (imageElement.naturalWidth > 0 && imageElement.naturalHeight > 0) {
        setLoaded({ source, width: imageElement.naturalWidth, height: imageElement.naturalHeight });
        setFailedSource(value => value === source ? "" : value);
      }
    };
    const fail = () => { if (active) setFailedSource(source); };
    imageElement.addEventListener("load", ready);
    imageElement.addEventListener("error", fail);
    // A cached/custom-protocol resource may finish before React attaches its
    // synthetic onLoad handler. Read the completed element synchronously and
    // also ask the decoder, so dimensions cannot be lost between effects.
    if (imageElement.complete) {
      if (imageElement.naturalWidth > 0) ready();
      else void imageElement.decode().then(ready, fail);
    } else {
      void imageElement.decode().then(ready, () => { /* native error event supplies the final state */ });
    }
    return () => {
      active = false;
      imageElement.removeEventListener("load", ready);
      imageElement.removeEventListener("error", fail);
    };
  }, [imageElement, source]);

  if (!mediaType) {
    return <Alert severity="error">Формат изображения не поддерживается. Для диагностики выберите режим HEX или Текст.</Alert>;
  }

  const availableWidth = Math.max(1, viewportSize.width - 32);
  const availableHeight = Math.max(1, viewportSize.height - 88);
  const scale = fit && natural ? imageFitScale(natural.width, natural.height, availableWidth, availableHeight) : manualScale;
  const displayWidth = natural ? Math.max(1, natural.width * scale) : 1;
  const displayHeight = natural ? Math.max(1, natural.height * scale) : 1;
  const canvasWidth = Math.max(availableWidth, displayWidth);
  const canvasHeight = Math.max(availableHeight, displayHeight);
  const changeZoom = (direction: -1 | 1) => {
    setManualScale(adjacentImageZoom(scale, direction));
    setFit(false);
  };

  return <Box aria-label="Просмотр изображения" data-image-canvas sx={{ minWidth: "100%", width: `${canvasWidth}px`,
    minHeight: `${canvasHeight}px`, ...checkerboard }}>
    <Box sx={{ position: "sticky", left: 0, top: 0, zIndex: 2, display: "flex", alignItems: "center", gap: .5,
      width: "fit-content", maxWidth: "calc(100vw - 48px)", p: .5, mb: 1, bgcolor: "background.paper",
      border: 1, borderColor: "divider", borderRadius: 1 }}>
      <Button size="small" variant={fit ? "contained" : "text"} onClick={() => setFit(true)}>Вписать</Button>
      <Button size="small" variant={!fit && manualScale === 1 ? "contained" : "text"}
        onClick={() => { setManualScale(1); setFit(false); }}>100%</Button>
      <Button size="small" aria-label="Уменьшить изображение" onClick={() => changeZoom(-1)} disabled={!fit && scale <= .25}>−</Button>
      <Button size="small" aria-label="Увеличить изображение" onClick={() => changeZoom(1)} disabled={!fit && scale >= 4}>+</Button>
      <Typography variant="body2" sx={{ minWidth: "4ch", textAlign: "right" }}>{imageZoomLabel(scale)}</Typography>
      {natural && <Typography variant="caption" color="text.secondary" sx={{ whiteSpace: "nowrap" }}>
        {natural.width} × {natural.height} px{mediaType === "image/gif" ? " · GIF-анимация" : ""}
      </Typography>}
    </Box>
    {failed && <Alert severity="error" sx={{ position: "sticky", left: 0, width: "min(620px, calc(100vw - 64px))" }}>
      Не удалось декодировать изображение. Файл повреждён либо формат не поддерживается текущим WebView.
    </Alert>}
    {!natural && !failed && <CircularProgress aria-label="Декодирование изображения" sx={{ position: "sticky", left: 24 }} />}
    <Box sx={{ width: `${canvasWidth}px`, height: `${canvasHeight}px`, display: "flex", alignItems: "center",
      justifyContent: "center" }}>
      <img ref={setImageElement} src={source} alt={name} draggable={false}
        onLoad={event => {
          const image = event.currentTarget;
          if (image.naturalWidth > 0 && image.naturalHeight > 0) {
            setLoaded({ source, width: image.naturalWidth, height: image.naturalHeight });
            setFailedSource(value => value === source ? "" : value);
          }
        }}
        onError={() => setFailedSource(source)}
        style={{ display: failed ? "none" : "block",
          width: natural ? `${displayWidth}px` : "auto", height: natural ? `${displayHeight}px` : "auto",
          maxWidth: "none", maxHeight: "none", imageRendering: scale >= 2 ? "auto" : undefined }} />
    </Box>
  </Box>;
}
