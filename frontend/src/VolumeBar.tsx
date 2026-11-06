import { useEffect, useRef, useState } from "react";
import { Box, Button, Divider, LinearProgress, Menu, MenuItem, Tooltip, Typography } from "@mui/material";
import { GetHomeDirectory, GetVolumeInfo, ListVolumes } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/volumeservice";
import type { Directory, HomeDirectory, Volume } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/models";
import { cancelVolumeCall, loadVolumeChoices, locationCaption, spacePresentation, type VolumeChoices } from "./volumePresentation";

type Props = { label: string; directory?: Directory; selectedHome?: HomeDirectory; choose: (location: string, volumeId?: string, home?: boolean) => void };

export function VolumeBar({ label, directory, selectedHome, choose }: Props) {
  const location = directory?.location;
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const [choices, setChoices] = useState<VolumeChoices>({ volumes: [], home: null, errors: [], loadingVolumes: false, loadingHome: false });
  const { volumes, home, loadingVolumes, loadingHome } = choices;
  const listError = choices.errors.join(" ");
  const [info, setInfo] = useState<{ location: string; volume?: Volume; error?: string }>();
  const menuVersion = useRef(0);
  const menuRequest = useRef<AbortController>();
  const [revision, setRevision] = useState(0);

  useEffect(() => () => { menuVersion.current++; menuRequest.current?.abort(); }, []);
  useEffect(() => {
    let current = true;
    let call: ReturnType<typeof GetVolumeInfo> | undefined;
    setInfo(undefined);
    if (location) {
      Promise.resolve().then(() => {
        if (!current) return;
        call = GetVolumeInfo({ path: "", location });
        return call;
      }).then(
        volume => { if (current && volume) setInfo({ location, volume }); },
        error => { if (current) setInfo({ location, error: error instanceof Error ? error.message : String(error) }); },
      );
    }
    return () => { current = false; cancelVolumeCall(call); };
    // A new snapshot also refreshes capacity when the path hasn't changed.
  }, [directory, location, revision]);

  async function open(element: HTMLElement) {
    menuRequest.current?.abort();
    const controller = new AbortController();
    menuRequest.current = controller;
    setAnchor(element);
    setChoices({ volumes: [], home: null, errors: [], loadingVolumes: true, loadingHome: true });
    setRevision(value => value + 1);
    const version = ++menuVersion.current;
    await loadVolumeChoices(ListVolumes, GetHomeDirectory, result => {
      if (version === menuVersion.current) setChoices(result);
    }, controller.signal);
  }
  function close() { menuVersion.current++; menuRequest.current?.abort(); setAnchor(null); }
  function select(location: string, volumeId?: string, home?: boolean) { close(); choose(location, volumeId, home); }

  // Never show the previous directory's capacity while a newer request runs.
  const current = info?.location === location ? info : undefined;
  const space = spacePresentation(current?.volume?.space);
  const caption = locationCaption(selectedHome, current?.volume);
  const warning = current?.error || current?.volume?.warning;

  return (
    <Box sx={{ px: 1, py: 0.5 }}>
      <Box sx={{ display: "flex", alignItems: "center", gap: 1, minWidth: 0 }}>
        <Tooltip title={caption}>
          <Button size="small" aria-label={`Выбор места: ${label}`} aria-haspopup="menu" aria-expanded={!!anchor}
            onClick={event => { void open(event.currentTarget); }} sx={{ minWidth: 100, maxWidth: "45%", justifyContent: "flex-start", textTransform: "none" }}>
            <Box component="span" sx={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{caption}</Box>
            <Box component="span" aria-hidden sx={{ ml: 0.5 }}>▾</Box>
          </Button>
        </Tooltip>
        <Tooltip title={warning || space.title}>
          <Typography sx={{ fontSize: 11, flex: 1, minWidth: 0 }}>{space.text}</Typography>
        </Tooltip>
        <Tooltip title={space.title}>
          <Box sx={{ width: 64, flexShrink: 0 }}>
            {space.percent === null
              ? <Box aria-label={`Объём неизвестен: ${label}`} sx={{ height: 5, borderRadius: 1, bgcolor: "action.disabledBackground" }} />
              : <LinearProgress aria-label={`Занято: ${label}`} variant="determinate" value={space.percent} sx={{ height: 5, borderRadius: 1 }} />}
          </Box>
        </Tooltip>
      </Box>
      {warning && <Typography role="status" sx={{ fontSize: 11, color: "text.secondary", overflowWrap: "anywhere" }}>{warning}</Typography>}
      <Menu anchorEl={anchor} open={!!anchor} onClose={close} slotProps={{ list: { "aria-label": `Места: ${label}` } }}>
        <MenuItem selected={!!selectedHome} disabled={!home} onClick={() => home && select(home.location, undefined, true)}>
          Home — {home?.displayPath ?? (loadingHome ? "Загрузка…" : "недоступен")}
        </MenuItem>
        <Divider />
        {loadingVolumes && <MenuItem disabled>Загрузка томов…</MenuItem>}
        {volumes.map(volume => <MenuItem key={volume.id} selected={!selectedHome && current?.volume?.id === volume.id} onClick={() => select(volume.rootLocation, volume.id)}
          title={volume.warning || undefined}>{volume.name} — {volume.displayPath}</MenuItem>)}
        {!loadingVolumes && !volumes.length && !listError && <MenuItem disabled>Нет доступных томов</MenuItem>}
        {listError && <MenuItem disabled sx={{ whiteSpace: "normal", maxWidth: 480 }}>{listError}</MenuItem>}
      </Menu>
    </Box>
  );
}
