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

export function ImageViewer({ fileID, name, mediaType, viewport }: ImageViewerProps) {
  const [natural, setNatural] = useState<ImageSize>();
  const [viewportSize, setViewportSize] = useState<ImageSize>({ width: 0, height: 0 });
  const [fit, setFit] = useState(true);
  const [manualScale, setManualScale] = useState(1);
  const [failed, setFailed] = useState(false);
  const source = useMemo(() => imageAssetURL(fileID, window.location), [fileID]);

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

  useEffect(() => { setNatural(undefined); setFailed(false); }, [source]);

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

  return <Box aria-label="Просмотр изображения" sx={{ minWidth: "100%", width: "max-content" }}>
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
    <Box sx={{ width: canvasWidth, height: canvasHeight, display: "flex", alignItems: "center", justifyContent: "center",
      backgroundColor: "background.default",
      backgroundImage: "linear-gradient(45deg, rgba(127,127,127,.14) 25%, transparent 25%), linear-gradient(-45deg, rgba(127,127,127,.14) 25%, transparent 25%), linear-gradient(45deg, transparent 75%, rgba(127,127,127,.14) 75%), linear-gradient(-45deg, transparent 75%, rgba(127,127,127,.14) 75%)",
      backgroundSize: "20px 20px", backgroundPosition: "0 0, 0 10px, 10px -10px, -10px 0" }}>
      <Box component="img" src={source} alt={name} draggable={false} onLoad={event => {
        const image = event.currentTarget;
        if (image.naturalWidth > 0 && image.naturalHeight > 0) {
          setNatural({ width: image.naturalWidth, height: image.naturalHeight });
          setFailed(false);
        } else setFailed(true);
      }} onError={() => { setNatural(undefined); setFailed(true); }} sx={{ display: failed ? "none" : "block",
        // MUI interprets numeric dimensions in the 0..1 range as fractions;
        // explicit CSS pixels keep 100% at one image pixel per CSS pixel.
        width: natural ? `${displayWidth}px` : "auto", height: natural ? `${displayHeight}px` : "auto",
        maxWidth: "none", maxHeight: "none",
        imageRendering: scale >= 2 ? "auto" : undefined }} />
    </Box>
  </Box>;
}
