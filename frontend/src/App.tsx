
import { Box, CssBaseline, Typography } from "@mui/material";
import { AgGridProvider, AgGridReact } from "ag-grid-react";
import {
  AllCommunityModule,
  type ColDef,
  type RowSelectionOptions,
} from "ag-grid-community";

type FileEntry = {
  path: string;
  name: string;
  size: number | null;
  type: "file" | "directory";
};

const columns: ColDef<FileEntry>[] = [
  { field: "name", headerName: "Имя", flex: 1 },
  {
    field: "size",
    headerName: "Размер",
    width: 110,
    valueFormatter: ({ value }) =>
      value == null ? "" : String(value),
  },
];

const selection: RowSelectionOptions = {
  mode: "multiRow",
  checkboxes: false,
  headerCheckbox: false,
  enableClickSelection: true,
};

const leftFiles: FileEntry[] = [
  { path: "/home/user/src", name: "src", size: null, type: "directory" },
  { path: "/home/user/docs", name: "docs", size: null, type: "directory" },
  { path: "/home/user/main.go", name: "main.go", size: 2048, type: "file" },
];

const rightFiles: FileEntry[] = [
  { path: "/tmp/build", name: "build", size: null, type: "directory" },
  { path: "/tmp/archive.zip", name: "archive.zip", size: 4096, type: "file" },
];

function FilePanel({
  path,
  files,
}: {
  path: string;
  files: FileEntry[];
}) {
  return (
    <Box
      sx={{
        flex: 1,
        minWidth: 0,
        minHeight: 0,
        display: "flex",
        flexDirection: "column",
        border: "1px solid #888",
      }}
    >
      <Typography
        sx={{
          padding: "6px 10px",
          fontSize: 13,
          fontWeight: 600,
        }}
      >
        {path}
      </Typography>

      <Box sx={{ flex: 1, minHeight: 0 }}>
        <AgGridReact<FileEntry>
          rowData={files}
          columnDefs={columns}
          getRowId={(params) => params.data.path}
          rowSelection={selection}
          rowHeight={28}
          headerHeight={32}
          animateRows={false}
        />
      </Box>
    </Box>
  );
}

export default function App() {
  return (
    <AgGridProvider modules={[AllCommunityModule]}>
      <CssBaseline />
      <Box
        sx={{
          height: "100vh",
          display: "flex",
          flexDirection: "column",
          padding: 1,
          gap: 1,
        }}
      >
        <Typography variant="h6">
          Marten Commander
        </Typography>

        <Box
          sx={{
            display: "flex",
            flex: 1,
            minHeight: 0,
            gap: 1,
          }}
        >
          <FilePanel path="/home/user" files={leftFiles} />
          <FilePanel path="/tmp" files={rightFiles} />
        </Box>
      </Box>
    </AgGridProvider>
  );
}
