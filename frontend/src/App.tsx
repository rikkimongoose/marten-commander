import { useState } from "react";
import { Box, CssBaseline } from "@mui/material";
import { AgGridProvider } from "ag-grid-react";
import { AllCommunityModule } from "ag-grid-community";
import { FilePanel } from "./FilePanel";

export default function App() {
  const [active, setActive] = useState<"left" | "right">("left");
  return (
    <AgGridProvider modules={[AllCommunityModule]}>
      <CssBaseline />
      <Box sx={{ height: "100vh", display: "flex", flexDirection: "column", p: 1, gap: 1 }}>
        <Box sx={{ display: "flex", flex: 1, minHeight: 0, gap: 1 }}>
          <FilePanel label="Левая панель" active={active === "left"} activate={() => setActive("left")} />
          <FilePanel label="Правая панель" active={active === "right"} activate={() => setActive("right")} />
        </Box>
      </Box>
    </AgGridProvider>
  );
}
