// Optional real-browser regression suite. Uses an externally installed
// playwright-core (PLAYWRIGHT_MODULE) and Chrome (CHROME_PATH), no app dependency.
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import react from "@vitejs/plugin-react";

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || "playwright-core");
const server = await createServer({
  configFile: false, root: fileURLToPath(new URL("../../", import.meta.url)), plugins: [react()],
  resolve: { alias: [{ find: /.*bindings\/.*\/desktop\/(commandservice|directoryservice|volumeservice)(\.[jt]s)?$/,
    replacement: fileURLToPath(new URL("navigationBackend.ts", import.meta.url)) }] },
  server: { host: "127.0.0.1", port: 0 },
});
let browser;
try {
  await server.listen();
  browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/opt/google/chrome/chrome", headless: true });
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(server.resolvedUrls.local[0]);
  const home = "/home/test";
  const panel = side => page.getByRole("region", { name: side === "left" ? "Левая панель" : "Правая панель" });
  const cell = (side, id) => panel(side).locator(`.ag-row[row-id="${id}"] .ag-cell[col-id="name"]`);
  const panelLabel = side => side === "left" ? "Левая панель" : "Правая панель";
  const pathButton = side => panel(side).getByRole("button", { name: `Изменить путь: ${panelLabel(side)}` });
  const path = async (side, value) => panel(side).locator(`[aria-label="Изменить путь: ${panelLabel(side)}"][title="${value}"]`).waitFor();
  const openPathEditor = async side => {
    await pathButton(side).click();
    return panel(side).getByRole("textbox", { name: `Путь: ${panelLabel(side)}` });
  };
  const focused = async (side, id) => page.waitForFunction(({ side, id }) => {
    const region = document.activeElement?.closest('[role="region"]');
    const row = document.activeElement?.closest(".ag-row");
    return region?.getAttribute("aria-label") === (side === "left" ? "Левая панель" : "Правая панель") &&
      row?.getAttribute("row-id") === id && row.classList.contains("cursor-row") &&
      region.querySelectorAll(".ag-row.cursor-row").length === 1;
  }, { side, id });
  const count = async (side, n) => panel(side).getByText(new RegExp(`выделено ${n}(?:$| ·)`)).waitFor();
  const functionBar = page.getByRole("navigation", { name: "Функциональные команды" });
  const commandForm = page.getByRole("form", { name: "Командная строка" });
  const functionButtons = functionBar.getByRole("button");
  assert.deepEqual(await functionButtons.allTextContents(), ["F3Просмотр", "F4Правка", "F5Копировать",
    "F6Переместить", "F7Каталог", "F8Удалить"]);
  for (let index = 0; index < 6; index++) assert.equal(await functionButtons.nth(index).isDisabled(), true);
  await page.setViewportSize({ width: 600, height: 900 });
  const compactBar = await functionBar.evaluate(element => ({ clientWidth: element.clientWidth,
    scrollWidth: element.scrollWidth, buttonWidths: [...element.querySelectorAll("button")].map(button => button.getBoundingClientRect().width) }));
  assert.ok(compactBar.scrollWidth > compactBar.clientWidth, "narrow bar scrolls instead of making labels unreadable");
  assert.ok(compactBar.buttonWidths.every(width => width >= 96), "function buttons retain their readable minimum width");
  const compactLayout = await Promise.all([panel("left").boundingBox(), commandForm.boundingBox(), functionBar.boundingBox()]);
  assert.ok(compactLayout.every(Boolean) && compactLayout[0].height > 200, "file list keeps the available height");
  assert.ok(compactLayout[1].y < compactLayout[2].y && compactLayout[2].y + compactLayout[2].height <= 900,
    "command line and function bar remain visible at the bottom");
  await page.setViewportSize({ width: 1400, height: 900 });
  const disabledOpens = await page.evaluate(() => window.navigationTest.opens.length);
  await page.keyboard.press("F3");
  assert.equal(await page.evaluate(() => window.navigationTest.opens.length), disabledOpens,
    "disabled shortcut does not dispatch a command");
  assert.equal(await page.getByRole("alert").count(), 0, "disabled shortcut reports no fake result or error");
  await path("left", home); await path("right", home);
  assert.equal(await panel("left").getByText(/^Левая панель:/).count(), 0);
  assert.equal(await panel("right").getByText(/^Правая панель:/).count(), 0);
  const commandInput = commandForm.getByRole("textbox", { name: "Команда" });
  await page.waitForFunction(home => document.querySelector('form[aria-label="Командная строка"] p')?.textContent === `${home} >`, home);
  await commandInput.fill("x"); await commandInput.press("Backspace"); assert.equal(await commandInput.inputValue(), "");
  assert.equal(await commandInput.evaluate(element => {
    const event = new KeyboardEvent("keydown", { key: "F3", bubbles: true, cancelable: true });
    element.dispatchEvent(event);
    return event.defaultPrevented;
  }), false, "function shortcuts do not capture command-line input");
  await commandInput.press("Tab");
  assert.equal(await panel("left").getAttribute("data-active"), "true", "Tab in command line does not switch panels");

  // Compact path header edits the panel path; Escape cancels and Enter uses
  // the same guarded panel loader.
  let pathInput = await openPathEditor("left");
  assert.equal(await pathInput.inputValue(), home);
  await pathInput.fill(`${home}/child`); await pathInput.press("Escape"); await path("left", home);
  pathInput = await openPathEditor("left");
  await pathInput.fill(`${home}/child`); await pathInput.press("Enter"); await path("left", `${home}/child`);
  await commandInput.fill("cd .."); await commandInput.press("Enter"); await path("left", home);

  // cd resolves relative paths against the active panel's opaque location,
  // supports quotes/absolute paths/Home, and never changes the other panel.
  await commandInput.fill("cd child"); await commandInput.press("Enter"); await path("left", `${home}/child`); await path("right", home);
  await page.waitForFunction(path => document.querySelector('form[aria-label="Командная строка"] p')?.textContent === `${path} >`, `${home}/child`);
  await cell("left", `${home}/child/nested`).click(); await focused("left", `${home}/child/nested`);
  await page.keyboard.press("Tab"); await focused("right", "..");
  await page.waitForFunction(home => document.querySelector('form[aria-label="Командная строка"] p')?.textContent === `${home} >`, home);
  await page.keyboard.press("Shift+Tab"); await focused("left", `${home}/child/nested`);
  await page.waitForFunction(path => document.querySelector('form[aria-label="Командная строка"] p')?.textContent === `${path} >`, `${home}/child`);
  await commandInput.fill("cd .."); await commandInput.press("Enter"); await path("left", home);
  await commandInput.fill('cd "My Projects"'); await commandInput.press("Enter"); await path("left", `${home}/My Projects`);
  await commandInput.fill("cd"); await commandInput.press("Enter"); await path("left", home);
  await commandInput.fill("cd /tmp"); await commandInput.press("Enter"); await path("left", "/tmp"); await path("right", home);
  await commandInput.fill("cd ~/child"); await commandInput.press("Enter"); await path("left", `${home}/child`);
  await commandInput.fill("cd ~/"); await commandInput.press("Enter"); await path("left", home);

  const safeReads = await page.evaluate(() => window.navigationTest.reads.length);
  await commandInput.fill("cd $(pwd)"); await commandInput.press("Enter");
  await page.getByText("Подстановки и операторы shell в cd не поддерживаются.").waitFor();
  assert.equal(await page.evaluate(() => window.navigationTest.reads.length), safeReads, "shell syntax never reaches directory reader");
  await commandInput.fill("cdrom /tmp"); await commandInput.press("Enter");
  await page.getByText("Выполнение внешних команд пока не поддерживается.").waitFor();
  assert.equal(await page.evaluate(() => window.navigationTest.reads.length), safeReads, "cdrom is not parsed as cd");
  await commandInput.press("ArrowUp"); assert.equal(await commandInput.inputValue(), "cdrom /tmp");
  await commandInput.press("ArrowUp"); assert.equal(await commandInput.inputValue(), "cd $(pwd)");
  await commandInput.press("ArrowDown"); assert.equal(await commandInput.inputValue(), "cdrom /tmp");
  await commandInput.press("ArrowDown"); assert.equal(await commandInput.inputValue(), "");

  await commandInput.fill("cd denied"); await commandInput.press("Enter");
  await panel("left").getByRole("alert").waitFor(); await path("left", home);
  await commandInput.fill("cd child"); await commandInput.press("Enter"); await path("left", `${home}/child`);
  await commandInput.fill("cd .."); await commandInput.press("Enter"); await path("left", home);
  await cell("left", "..").click();
  await focused("left", "..");

  // A plain click selects the current row; repeating that click toggles off
  // its sole explicit selection without moving the independent cursor.
  await cell("left", `${home}/child`).click(); await focused("left", `${home}/child`); await count("left", 1);
  await cell("left", `${home}/child`).click(); await focused("left", `${home}/child`); await count("left", 0);
  await page.keyboard.press("Tab"); await focused("right", "..");
  await page.keyboard.press("Tab"); await focused("left", `${home}/child`);

  // Double click/Enter/down/up; returned cursor is independent of selection.
  await cell("left", `${home}/child`).dblclick(); await path("left", `${home}/child`);
  await path("right", home); await count("left", 0);
  await cell("left", "..").click(); await page.keyboard.press("Enter");
  await path("left", home); await focused("left", `${home}/child`); await count("left", 0);
  await page.keyboard.press("Enter"); await path("left", `${home}/child`);
  await page.keyboard.press("Backspace"); await path("left", home); await focused("left", `${home}/child`);
  await cell("left", `${home}/child`).dblclick(); await path("left", `${home}/child`);
  await cell("left", "..").dblclick(); await path("left", home); await focused("left", `${home}/child`);

  // Modifier-clicks select arbitrary rows without moving the independent
  // keyboard cursor. Ctrl toggles individual rows; Shift selects a range.
  await cell("left", `${home}/one.txt`).click({ modifiers: ["Control"] }); await count("left", 1);
  await focused("left", `${home}/child`);
  await cell("left", `${home}/two.txt`).click({ modifiers: ["Control"] }); await count("left", 2);
  await focused("left", `${home}/child`);
  assert.match(await cell("left", `${home}/one.txt`).evaluate(element => getComputedStyle(element).color), /211, 47, 47/,
    "explicitly selected rows are red");
  assert.deepEqual(await panel("left").locator(".ag-row.ag-row-selected").evaluateAll(rows => rows.map(row => row.getAttribute("row-id")).sort()),
    [`${home}/one.txt`, `${home}/two.txt`], "Ctrl selects arbitrary rows without moving the cursor");
  await cell("left", `${home}/one.txt`).click();
  await focused("left", `${home}/one.txt`);
  await cell("left", `${home}/two.txt`).click({ modifiers: ["Shift"] }); await count("left", 3);
  await focused("left", `${home}/one.txt`);
  assert.deepEqual(await panel("left").locator(".ag-row.ag-row-selected").evaluateAll(rows => rows.map(row => row.getAttribute("row-id")).sort()),
    [`${home}/one.txt`, `${home}/three.txt`, `${home}/two.txt`], "Shift selects the complete range without moving the cursor");
  await page.keyboard.press("ArrowUp"); await focused("left", `${home}/My Projects`); await count("left", 3);
  await page.keyboard.press("ArrowDown"); await focused("left", `${home}/one.txt`); await count("left", 3);
  const reads = await page.evaluate(() => window.navigationTest.reads.length);
  const opens = await page.evaluate(() => window.navigationTest.opens.length);
  await page.keyboard.press("Enter");
  await page.waitForFunction(count => window.navigationTest.opens.length === count + 1, opens);
  assert.equal(await page.evaluate(() => window.navigationTest.opens.at(-1).type), "file.open");
  assert.equal(await page.evaluate(() => window.navigationTest.opens.at(-1).params.location), `${home}/one.txt`);
  assert.equal(await page.evaluate(() => window.navigationTest.reads.length), reads, "opening a file does not navigate");
  await count("left", 3);
  await cell("left", `${home}/two.txt`).click({ modifiers: ["Control"] }); await count("left", 2);
  await focused("left", `${home}/one.txt`);
  await cell("left", `${home}/two.txt`).dblclick();
  await page.waitForFunction(count => window.navigationTest.opens.length === count + 2, opens);
  assert.equal(await page.evaluate(() => window.navigationTest.opens.at(-1).params.location), `${home}/two.txt`);
  assert.equal(await page.evaluate(() => window.navigationTest.reads.length), reads, "double-click uses file.open instead of navigation");
  await focused("left", `${home}/two.txt`); await count("left", 2);
  assert.deepEqual(await panel("left").locator(".ag-row.ag-row-selected").evaluateAll(rows => rows.map(row => row.getAttribute("row-id")).sort()),
    [`${home}/one.txt`, `${home}/three.txt`], "double-click does not add the opened file to explicit selection");
  await page.keyboard.press("Control+a"); await count("left", 6);
  assert.notEqual(await panel("left").locator('.ag-row[row-id=".."]').getAttribute("aria-selected"), "true");
  await page.evaluate(() => { window.navigationTest.openError = "Нет системного обработчика для файла."; });
  await page.keyboard.press("Enter");
  await page.getByText("Нет системного обработчика для файла.").waitFor();
  await path("left", home); await focused("left", `${home}/two.txt`); await count("left", 6);
  await page.evaluate(() => { window.navigationTest.openError = ""; });

  await page.keyboard.press("Tab"); await focused("right", "..");
  assert.equal(await panel("right").getAttribute("data-active"), "true");
  assert.equal(await panel("left").getAttribute("data-active"), "false");
  await page.keyboard.press("Shift+Tab"); await focused("left", `${home}/two.txt`); await count("left", 6);
  await page.keyboard.press("Control+r");
  await panel("left").getByRole("progressbar", { name: "Чтение: Левая панель" }).waitFor({ state: "hidden" });
  await focused("left", `${home}/two.txt`); await count("left", 6);
  await cell("left", `${home}/child`).dblclick(); await path("left", `${home}/child`); await count("left", 0);
  await page.keyboard.press("Backspace"); await path("left", home); await count("left", 0);

  // Denied read retains path/rows and leaves keyboard navigation usable.
  const rowNames = await panel("left").locator('.ag-cell[col-id="name"]').allTextContents();
  await cell("left", `${home}/denied`).dblclick();
  await panel("left").getByRole("alert").waitFor(); await path("left", home);
  assert.deepEqual(await panel("left").locator('.ag-cell[col-id="name"]').allTextContents(), rowNames);
  await cell("left", `${home}/child`).dblclick(); await path("left", `${home}/child`);
  await page.keyboard.press("Backspace"); await path("left", home);

  // An inactive panel finishing a request must not steal the other panel's focus.
  await page.evaluate(home => { window.navigationTest.hold = `${home}/child`; }, home);
  await cell("left", `${home}/child`).dblclick();
  await page.keyboard.press("Tab"); await focused("right", "..");
  await page.evaluate(() => { window.navigationTest.hold = ""; window.navigationTest.release(); });
  await path("left", `${home}/child`); await focused("right", "..");

  // Text editing must retain native Backspace/Tab behavior.
  const input = await openPathEditor("left");
  await input.fill("/tmp/x"); await input.press("Backspace"); assert.equal(await input.inputValue(), "/tmp/");
  assert.equal(await input.evaluate(element => {
    const event = new KeyboardEvent("keydown", { key: "F3", bubbles: true, cancelable: true });
    element.dispatchEvent(event);
    return event.defaultPrevented;
  }), false, "function keys retain native behavior in text input");
  assert.equal(await page.evaluate(() => {
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    document.body.append(dialog);
    const event = new KeyboardEvent("keydown", { key: "F4", bubbles: true, cancelable: true });
    document.body.dispatchEvent(event);
    dialog.remove();
    return event.defaultPrevented;
  }), false, "function keys retain modal behavior");
  assert.equal(await page.evaluate(() => {
    const editor = document.createElement("div");
    editor.className = "ag-cell-editing";
    document.body.append(editor);
    const event = new KeyboardEvent("keydown", { key: "F5", bubbles: true, cancelable: true });
    document.body.dispatchEvent(event);
    editor.remove();
    return event.defaultPrevented;
  }), false, "function keys retain AG Grid editor behavior");
  await input.press("Tab");
  assert.equal(await panel("left").getAttribute("data-active"), "true");

  // Empty filesystem root has no '..'; Tab still reaches/leaves its file list.
  await page.evaluate(() => { window.navigationTest.emptyRoot = true; });
  const rootInput = await openPathEditor("left");
  await rootInput.fill("/"); await rootInput.press("Enter"); await path("left", "/");
  assert.equal(await cell("left", "..").count(), 0);
  assert.equal(await panel("left").getByRole("button", { name: "↑ Вверх" }).isDisabled(), true);
  await cell("right", "..").click(); await page.keyboard.press("Tab");
  const emptyList = panel("left").getByRole("group", { name: "Файлы: Левая панель" });
  assert.equal(await emptyList.evaluate(element => element === document.activeElement), true);
  const rootReads = await page.evaluate(() => window.navigationTest.reads.length);
  await page.keyboard.press("Backspace");
  assert.equal(await page.evaluate(() => window.navigationTest.reads.length), rootReads);
  await page.keyboard.press("Tab"); await focused("right", "..");
  // firstDataRendered is not fired by AG Grid when startup has no rows at all.
  await page.goto(`${server.resolvedUrls.local[0]}?emptyRoot=1`);
  await path("left", "/"); await path("right", "/");
  await page.waitForFunction(() => document.activeElement?.getAttribute("aria-label") === "Файлы: Левая панель");
  await page.keyboard.press("Tab");
  await page.waitForFunction(() => document.activeElement?.getAttribute("aria-label") === "Файлы: Правая панель");
  // Recover from the initially empty list through ordinary typed navigation.
  const recoveryInput = await openPathEditor("right");
  await recoveryInput.fill(home); await recoveryInput.press("Enter"); await path("right", home);
  await cell("right", `${home}/child`).dblclick(); await path("right", `${home}/child`);
  await page.keyboard.press("Backspace"); await path("right", home); await focused("right", `${home}/child`);

  // Ctrl-clicking a row in the inactive panel activates that panel and makes
  // the clicked row both current and explicitly selected.
  await page.keyboard.press("Tab");
  assert.equal(await panel("left").getAttribute("data-active"), "true");
  await cell("right", `${home}/one.txt`).click({ modifiers: ["Control"] });
  assert.equal(await panel("right").getAttribute("data-active"), "true");
  await focused("right", `${home}/one.txt`); await count("right", 1);
  await page.waitForFunction(home => document.querySelector('form[aria-label="Командная строка"] p')?.textContent === `${home} >`, home);
  assert.equal(await panel("right").locator(`.ag-row[row-id="${home}/one.txt"]`).getAttribute("aria-selected"), "true");
  assert.deepEqual(errors, []);
  console.log("PASS: real AG Grid navigation, cursor, Ctrl/Shift selection, Tab, root, permission errors and async focus");
} finally {
  await browser?.close();
  await server.close();
}
