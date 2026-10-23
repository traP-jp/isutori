import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join, resolve } from "node:path";
import { chromium, type Browser, type Page } from "playwright";

declare global {
  interface Window {
    __isutoriMenus: Map<string, () => void | Promise<void>>;
  }
}
interface Entry {
  page: string;
  sequence: number;
  reason: string;
  url: string;
  snapshot: string;
  target?: string;
  userscript: {expected:string;actual:string;status:string;error?:string};
}
const repo = fileURLToPath(new URL("../../", import.meta.url));
mkdirSync(join(repo, ".claude"), { recursive: true });
const work = mkdtempSync(join(repo, ".claude/inspect-browser-"));
execFileSync("go", ["build", "-o", ".cache/isutori", ".."], { stdio: "inherit" });
const binary = resolve(".cache/isutori");
const portal = "http://portal.example";
const userscript = execFileSync(binary, ["userscript", `${portal}/*`], { encoding: "utf8" });
const scriptPath=join(work,"script.user.js");
writeFileSync(scriptPath,userscript);
const child = spawn(binary, ["inspect", "--userscript",scriptPath,"--listen", "127.0.0.1:0", `${portal}/contestant`], { cwd: work, stdio: ["ignore", "ignore", "pipe"] });
const exited = new Promise<void>((resolve) => child.once("close", () => resolve()));
let browser: Browser | undefined;
let output = "";
let connection = "";
let directory = "";
let failPosts = false;
let requests = 0;
const fixture = `<!doctype html><html><body>
<button id="open">Open options</button><div id="options"></div>
<select><option value="1">1: 192.168.0.11</option><option value="2">2: 192.168.0.12</option></select>
<button>Enqueue</button><button id="finish">Finish</button>
<div id="status">RUNNING</div><div id="score">0</div>
<input id="live" value="old"><input id="check" type="checkbox"><input type="password" value="secret-password"><input type="hidden" value="secret-hidden">
<textarea>old text</textarea>
<script>
document.querySelector('#open').onclick=()=>{
 document.querySelector('#options').innerHTML='<div role="option">No available options</div>';
 setTimeout(()=>document.querySelector('#options').innerHTML='<div role="option">Server ready</div>',250);
};
document.querySelector('#finish').onclick=()=>{
 document.querySelector('#status').textContent='FINISHED';
 setTimeout(()=>document.querySelector('#score').textContent='42',250);
};
</script></body></html>`;
function entries(): Entry[] {
  return readFileSync(join(directory, "events.jsonl"), "utf8").split("\n").slice(0, -1).filter(Boolean).map((line) => JSON.parse(line));
}
function html(entry: Entry) { return readFileSync(join(directory, entry.snapshot), "utf8"); }
async function until(check: () => boolean, description: string) {
  const deadline = Date.now() + 10000;
  while (!check()) {
    if (Date.now() > deadline) throw new Error(`timed out: ${description}`);
    await new Promise((resolve) => setTimeout(resolve, 30));
  }
}
async function menu(page: Page, name: string, answer?: string) {
  const dialog = async (dialog: import("playwright").Dialog) => {
    if (dialog.type() === "prompt") await dialog.accept(answer ?? "");
    else await dialog.dismiss();
  };
  page.on("dialog", dialog);
  try {
    await page.waitForFunction((name) => window.__isutoriMenus?.has(name), name);
    await page.evaluate(async (name) => {
      const callback = window.__isutoriMenus.get(name);
      if (!callback) throw new Error(`missing menu: ${name}`);
      await callback();
    }, name);
  } finally { page.off("dialog", dialog); }
}
try {
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error(`inspect startup failed: ${output}`)), 10000);
    child.once("error", (error) => { clearTimeout(timeout); reject(error); });
    child.once("exit", () => { clearTimeout(timeout); reject(new Error(`inspect exited: ${output}`)); });
    child.stderr.on("data", (chunk) => {
      output += chunk;
      const address = output.match(/http:\/\/127\.0\.0\.1:\d+\/#[A-Za-z0-9_-]+/);
      const path = output.match(/inspection directory: (.+)\n/);
      if (address && path) { connection = address[0]; directory = path[1]; clearTimeout(timeout); resolve(); }
    });
  });
  browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  context.on("page", (page) => {
    page.on("pageerror", (error) => { throw error; });
    page.on("domcontentloaded", async () => {
      if (new URL(page.url()).origin === portal) await page.evaluate(userscript);
    });
  });
  await context.route(`${portal}/**`, (route) => route.fulfill({ contentType: "text/html", body: fixture }));
  await context.exposeBinding("__inspectRequest", async (_source, request: { url: string; method: string; headers: Record<string, string>; data?: string }) => {
    requests++;
    if (failPosts && request.method === "POST") return null;
    try {
      const response = await fetch(request.url, { method: request.method, headers: request.headers, body: request.data });
      return { status: response.status, responseText: await response.text() };
    } catch { return null; }
  });
  await context.addInitScript(`
    window.__isutoriMenus ??= new Map();
    window.GM_registerMenuCommand=(name, callback)=>window.__isutoriMenus.set(name,callback);
    window.GM_xmlhttpRequest=(request)=>window.__inspectRequest(request).then(response=>response ? request.onload(response) : request.onerror());
  `);
  const page = await context.newPage();
  await page.goto(`${portal}/contestant?private=omit#private`);
  assert.equal(requests, 0);
  assert.equal(await page.evaluate(() => isSecureContext), false);
  await menu(page, "isutori: start recording", connection);
  await until(() => entries().length > 0, "initial snapshot");
  assert.equal(await page.evaluate(() => sessionStorage.getItem("isutori-autobench-v2")), null);
  assert.equal(entries()[0].userscript.status,"matched");
  const first = html(entries()[0]);
  assert.ok(!first.includes("secret-password") && !first.includes("secret-hidden"));
  assert.ok(!first.includes("<script") && !first.includes("isutori recording:"));
  assert.equal(entries()[0].url, `${portal}/contestant`);

  await page.locator("#open").click();
  await until(() => entries().some((entry) => html(entry).includes("No available options")), "loading state");
  await until(() => entries().some((entry) => html(entry).includes("Server ready")), "loaded state");
  assert.ok(entries().some((entry) => entry.reason === "click:after" && entry.target?.includes("open") && html(entry).includes("No available options")));
  await page.locator("#finish").click();
  await until(() => entries().some((entry) => html(entry).includes('id="status">FINISHED') && html(entry).includes('id="score">0')), "finished before score");
  await until(() => entries().some((entry) => html(entry).includes('id="score">42')), "updated score");
  await page.locator("#live").evaluate((element) => { if (element instanceof HTMLInputElement) element.value = "property-only"; });
  await until(() => entries().some((entry) => entry.reason === "tick" && html(entry).includes('value="property-only"')), "periodic property capture");
  await page.locator("textarea").fill("live textarea");
  await page.locator("select").selectOption("2");
  await page.locator("#check").check();
  await page.locator("#check").evaluate((element) => { if (element instanceof HTMLInputElement) element.indeterminate = true; });
  await until(() => entries().some((entry) => {
    const body = html(entry);
    return body.includes('value="property-only"') && body.includes("live textarea") && /value="2"[^>]*selected/.test(body) && /id="check"[^>]*checked/.test(body) && body.includes("data-indeterminate");
  }), "live form properties");
  const click = entries().find((entry) => entry.reason === "click" && entry.target?.includes("open"));
  assert.ok(click?.target);
  assert.equal(JSON.parse(click.target).path, "body > button:nth-child(1)");
  const pageID = entries()[0].page;
  await page.reload();
  await until(() => entries().some((entry) => entry.page !== pageID && entry.reason === "start"), "recording after reload");
  const popupWait = page.waitForEvent("popup");
  await page.evaluate((url) => { window.open(url, "_blank"); }, `${portal}/contestant`);
  const popup = await popupWait;
  await popup.waitForLoadState("load");
  await popup.waitForFunction(() => window.__isutoriMenus?.has("isutori: start recording"));
  await popup.waitForFunction(() => sessionStorage.getItem("isutori-inspect") === null);
  await popup.close();
  const another = await context.newPage();
  const beforeNewTab = entries().length;
  await another.goto(`${portal}/contestant`);
  assert.equal(await another.evaluate(() => sessionStorage.getItem("isutori-inspect")), null);
  await another.close();
  assert.ok(entries().slice(beforeNewTab).every((entry) => entry.reason !== "start"));

  writeFileSync(scriptPath,userscript.replace("async prepare(target)","async prepare(target /* changed reference */)"));
  await until(()=>entries().some(entry=>entry.userscript.status==="mismatched"),"reference change");
  await page.getByText(/recording; mismatched/).waitFor();
  writeFileSync(scriptPath,"incomplete file being edited");
  await until(()=>entries().some(entry=>entry.userscript.status==="unknown"),"invalid reference");
  writeFileSync(scriptPath,userscript);
  await page.getByText(/recording; matched/).waitFor();
  failPosts = true;
  await page.locator("#open").click();
  await page.getByText(/STOPPED: receiver disconnected/).waitFor();
  assert.equal(await page.evaluate(() => sessionStorage.getItem("isutori-inspect")), null);
  failPosts = false;
  await menu(page, "isutori: start recording", connection);
  await page.getByText(/isutori recording: recording/).waitFor();
  await menu(page, "isutori: stop recording");
  await page.getByText("isutori recording: stopped; 0 event(s) pending", { exact: true }).waitFor();
  const count = entries().length;
  await page.locator("#finish").click();
  await page.waitForTimeout(1200);
  assert.equal(entries().length, count);
  await page.reload();
  assert.equal(await page.evaluate(() => sessionStorage.getItem("isutori-inspect")), null);
  await page.waitForTimeout(1100);
  assert.equal(entries().length, count);
  await menu(page, "isutori: start recording", connection);
  await page.getByText(/isutori recording: recording/).waitFor();
  await page.evaluate(() => { const options = document.querySelector("#options"); if (options) options.textContent = "x".repeat(2 * 1024 * 1024); });
  await page.getByText(/STOPPED: recording capacity reached/).waitFor();
  assert.equal(await page.evaluate(() => sessionStorage.getItem("isutori-inspect")), null);
  const sourceless = await context.newPage();
  await sourceless.goto(`${portal}/contestant`);
  await sourceless.waitForFunction(() => window.__isutoriMenus?.has("isutori: start recording"));
  await sourceless.evaluate(() => {
    const original = Function.prototype.toString;
    Function.prototype.toString = function (this: { name: string }) {
      return this.name === "isutoriScript" ? "function isutoriScript() { [native code] }" : original.call(this);
    };
  });
  await menu(sourceless, "isutori: start recording", connection);
  await sourceless.getByText(/recording; unknown/).waitFor();
  await sourceless.locator("#open").click();
  await until(() => entries().some(entry => entry.reason === "click" && entry.userscript.status === "unknown" && entry.userscript.actual === ""), "recording without source");
  await menu(sourceless, "isutori: stop recording");
  await sourceless.getByText("isutori recording: stopped; 0 event(s) pending", { exact: true }).waitFor();
  await sourceless.close();
  console.log(`inspect: OK; ${count} events; artifacts: ${directory}`);
} finally {
  try { await browser?.close(); }
  finally { child.kill("SIGINT"); await exited; }
}
