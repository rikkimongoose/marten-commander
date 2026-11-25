// Real-browser regression for style.yaml colour precedence over AG Grid's
// cursor and explicit row selection.
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
  const panel = side => page.getByRole("region", { name: side === "left" ? "Левая панель" : "Правая панель" });
  const cell = (side, id) => panel(side).locator(`.ag-row[row-id="${id}"] .ag-cell[col-id="name"]`);
  const colour = (side, id) => cell(side, id).evaluate(element => getComputedStyle(element).color);
  const waitColour = async (side, id, expected, matches = true) => {
    const label = side === "left" ? "Левая панель" : "Правая панель";
    await page.waitForFunction(({ label, id, expected, matches }) => {
      const region = [...document.querySelectorAll('[role="region"]')]
        .find(element => element.getAttribute("aria-label") === label);
      const row = [...(region?.querySelectorAll(".ag-row") ?? [])].find(element => element.getAttribute("row-id") === id);
      const value = row?.querySelector('.ag-cell[col-id="name"]');
      const present = !!value && new RegExp(expected).test(getComputedStyle(value).color);
      return matches ? present : !!value && !!getComputedStyle(value).color && !present;
    }, { label, id, expected: expected.source, matches });
    return colour(side, id);
  };
  const selected = (side, count) => page.waitForFunction(({ label, count }) =>
    [...document.querySelectorAll('[role="region"]')].find(element => element.getAttribute("aria-label") === label)
      ?.querySelectorAll(".ag-row.ag-row-selected").length === count,
  { label: side === "left" ? "Левая панель" : "Правая панель", count });
  await cell("left", `${home}/two.txt`).waitFor(); await cell("right", `${home}/two.txt`).waitFor();
  assert.match(await waitColour("left", `${home}/two.txt`, /0, 170, 255/), /0, 170, 255/, "style colour is initially visible");

  await cell("left", `${home}/child`).click(); await selected("left", 1);
  assert.match(await waitColour("left", `${home}/child`, /211, 47, 47/), /211, 47, 47/, "a selected directory uses selected text colour");
  await cell("left", `${home}/child`).click(); await selected("left", 0);
  assert.doesNotMatch(await waitColour("left", `${home}/child`, /211, 47, 47/, false), /211, 47, 47/, "a deselected directory returns to normal");

  await cell("left", `${home}/two.txt`).click(); await selected("left", 1);
  assert.match(await waitColour("left", `${home}/two.txt`, /211, 47, 47/), /211, 47, 47/, "single selection overrides the style colour");
  await cell("left", `${home}/two.txt`).click(); await selected("left", 0);
  assert.match(await waitColour("left", `${home}/two.txt`, /0, 170, 255/), /0, 170, 255/,
    "deselection restores the style colour while the keyboard cursor stays on the row");
  await page.keyboard.press("ArrowUp");
  assert.match(await waitColour("left", `${home}/two.txt`, /0, 170, 255/), /0, 170, 255/, "moving only the cursor does not remove the style colour");

  await cell("left", `${home}/one.txt`).click({ modifiers: ["Control"] });
  await cell("left", `${home}/two.txt`).click({ modifiers: ["Control"] }); await selected("left", 2);
  assert.match(await waitColour("left", `${home}/two.txt`, /211, 47, 47/), /211, 47, 47/, "multiple selection overrides the style colour");
  await cell("left", `${home}/two.txt`).click({ modifiers: ["Control"] }); await selected("left", 1);
  assert.match(await waitColour("left", `${home}/two.txt`, /0, 170, 255/), /0, 170, 255/, "removing one row from selection restores its style");

  await cell("right", `${home}/two.txt`).click(); await selected("right", 1);
  assert.match(await waitColour("right", `${home}/two.txt`, /211, 47, 47/), /211, 47, 47/, "the right panel uses the same selected text colour");
  await cell("right", `${home}/two.txt`).click(); await selected("right", 0);
  assert.match(await waitColour("right", `${home}/two.txt`, /0, 170, 255/), /0, 170, 255/, "the right panel restores the style after deselection");
  console.log("PASS: explicit selection overrides style.yaml colours in both panels");
} finally {
  await browser?.close();
  await server.close();
}
