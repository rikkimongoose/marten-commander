import { Button, Dialog, DialogActions, DialogContent, DialogTitle, List, ListItem, Typography } from "@mui/material";
import type { TrashPreview } from "./trashCommand";

export function TrashDialog({ preview, confirm, cancel, closed }: {
  preview: TrashPreview | null; confirm: () => void; cancel: () => void; closed: () => void;
}) {
  return <Dialog open={preview !== null} onClose={cancel}
    slotProps={{ transition: { onExited: () => queueMicrotask(closed) } }}>
    <DialogTitle>Переместить в корзину?</DialogTitle>
    <DialogContent sx={{ minWidth: { xs: 300, sm: 520 } }}>
      <Typography>Объектов: {preview?.entries.length ?? 0}</Typography>
      <Typography sx={{ overflowWrap: "anywhere" }}>Исходный каталог: {preview?.directory}</Typography>
      <List dense sx={{ maxHeight: 240, overflow: "auto" }}>
        {preview?.entries.map(entry => <ListItem key={entry.location} sx={{ display: "block", overflowWrap: "anywhere" }}>
          {entry.name}{entry.kind === "directory" && " — каталог целиком, включая содержимое"}
          {entry.kind === "symlink" && " — символическая ссылка"}
        </ListItem>)}
      </List>
      <Typography>Объекты будут перемещены в системную корзину. Их можно восстановить средствами ОС.</Typography>
    </DialogContent>
    <DialogActions>
      <Button autoFocus onClick={cancel}>Отмена</Button>
      <Button variant="contained" onClick={confirm}>Переместить в корзину</Button>
    </DialogActions>
  </Dialog>;
}
