import { useEffect, useState } from "react";
import { Alert, Box, Button, CircularProgress, CssBaseline } from "@mui/material";
import { Window } from "@wailsio/runtime";
import type { ViewerFile } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/models";
import { GetViewer } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/viewerwindowservice";
import { commandErrorMessage } from "./commands";
import { Viewer } from "./Viewer";

export function ViewerWindow({ viewerID }: { viewerID: string }) {
  const [file, setFile] = useState<ViewerFile>();
  const [error, setError] = useState("");
  const closeNative = () => {
    void Window.Close().catch(reason => setError(`Не удалось закрыть нативное окно: ${commandErrorMessage(reason)}`));
  };

  useEffect(() => {
    let disposed = false;
    const request = GetViewer(viewerID);
    request.then(value => {
      if (!disposed) setFile(value);
    }).catch(reason => {
      if (!disposed && (!(reason instanceof Error) || reason.name !== "CancelError")) setError(commandErrorMessage(reason));
    });
    return () => {
      disposed = true;
      void request.cancel("viewer window unmounted");
    };
  }, [viewerID]);

  return <>
    <CssBaseline />
    <Box sx={{ position: "relative", width: "100vw", height: "100vh", bgcolor: "background.default" }}>
      {file ? <>
        <Viewer file={file} reportCloseError={setError} close={closeNative} />
        {error && <Alert severity="error" action={<Button color="inherit" onClick={closeNative}>Повторить</Button>}
          sx={{ position: "absolute", zIndex: 30, left: 16, right: 16, bottom: 16 }}>{error}</Alert>}
      </>
        : error ? <Box sx={{ p: 2 }}>
          <Alert severity="error" action={<Button color="inherit" onClick={closeNative}>Закрыть</Button>}>
            {error}
          </Alert>
        </Box>
        : <CircularProgress aria-label="Открытие Viewer" sx={{ position: "absolute", left: "50%", top: "50%" }} />}
    </Box>
  </>;
}
