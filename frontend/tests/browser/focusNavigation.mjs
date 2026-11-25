// Focus regression for Issue #27. This is intentionally a real-browser test:
// AG Grid's logical focus and document.activeElement cannot be verified in the
// Node unit-test environment.
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import react from "@vitejs/plugin-react";

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || "playwright-core");
const server = await createServer({
  configFile: false, root: fileURLToPath(new URL("../../", import.meta.url)), plugins: [react()],
  resolve: { alias: [{ find: /.*bindings\/.*\/desktop\/(commandservice|directoryservice|operationservice|sftpservice|volumeservice|viewerservice|viewerwindowservice)(\.[jt]s)?$/,
    replacement: fileURLToPath(new URL("navigationBackend.ts", import.meta.url)) }] },
  server: { host: "127.0.0.1", port: 0 },
});
let browser;
try {
  await server.listen();
  browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/opt/google/chrome/chrome", headless: true });
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  await page.goto(server.resolvedUrls.local[0]);
  const home = "/home/test";
  const label = side => side === "left" ? "Левая панель" : "Правая панель";
  const panel = side => page.getByRole("region", { name: label(side) });
  const cell = (side, id) => panel(side).locator(`.ag-row[row-id="${id}"] .ag-cell[col-id="name"]`);
  const path = (side, value) => panel(side).locator(`[aria-label="Изменить путь: ${label(side)}"][title="${value}"]`).waitFor();
  const focused = (side, id) => page.waitForFunction(({ side, id }) => {
    const region = document.activeElement?.closest('[role="region"]');
    const row = document.activeElement?.closest(".ag-row");
    return region?.getAttribute("aria-label") === (side === "left" ? "Левая панель" : "Правая панель") &&
      row?.getAttribute("row-id") === id && row.classList.contains("cursor-row");
  }, { side, id });
  await path("left", home); await path("right", home);

  // Give both panels the same remembered directory, then exercise switching
  // and navigation in both directions exclusively through keyboard events.
  await cell("right", `${home}/child`).click();
  await cell("left", `${home}/child`).click();
  for (const side of ["right", "left"]) {
    await page.keyboard.press("Tab"); await focused(side, `${home}/child`);
    await page.keyboard.press("Enter"); await path(side, `${home}/child`); await focused(side, "..");
    await page.keyboard.press("ArrowDown"); await focused(side, `${home}/child/nested`);
    await page.keyboard.press("ArrowUp"); await focused(side, "..");
    await page.keyboard.press("Backspace"); await path(side, home); await focused(side, `${home}/child`);
  }

  // A late result must not take focus back from an input in the other panel.
  await page.evaluate(home => { window.navigationTest.hold = `${home}/child`; }, home);
  await page.keyboard.press("Enter");
  await panel("right").getByRole("button", { name: `Изменить путь: ${label("right")}` }).click();
  const input = panel("right").getByRole("textbox", { name: `Путь: ${label("right")}` });
  await input.waitFor();
  await page.evaluate(() => { window.navigationTest.hold = ""; window.navigationTest.release(); });
  await path("left", `${home}/child`);
  assert.equal(await input.evaluate(element => element === document.activeElement), true,
    "a late directory result must not steal focus from another panel input");
  console.log("PASS: Tab -> Enter -> ArrowDown -> ArrowUp -> Backspace keeps focus in both directions");
} finally {
  await browser?.close();
  await server.close();
}
