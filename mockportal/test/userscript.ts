// Runs the userscript (with its example adapter) against `isutori mock-portal`
// and checks what it reports to a stand-in for the isutori receiver.
import assert from "node:assert/strict";
import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { createServer } from "node:http";
import { chromium, type Browser, type Dialog, type Page } from "playwright";

declare global {
  interface Window {
    __isutoriMenus: Map<string, () => void | Promise<void>>;
  }
}

interface Result {
  run_id: string;
  passed?: boolean;
  score?: number;
  message?: string;
  error?: string;
}

interface GMRequest {
  url: string;
  method: string;
  headers: Record<string, string>;
  data: string;
}

execFileSync("go", ["build", "-o", ".cache/isutori", ".."], { stdio: "inherit" });
const userscript = execFileSync(".cache/isutori", ["userscript"], { encoding: "utf8" });

const portals: ChildProcess[] = [];
function startPortal(port: number, outcome: "pass" | "fail" | "error", running = "2s") {
  portals.push(
    spawn(".cache/isutori", ["mock-portal", "--listen", `127.0.0.1:${port}`, "--pending", "1s", "--running", running, "--outcome", outcome], {
      stdio: "ignore",
    }),
  );
  return `http://127.0.0.1:${port}`;
}

const posted = new Map<string, (r: Result) => void>();
let receivedRequests = 0;
const receiver = createServer((req, res) => {
  receivedRequests++;
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    const result: Result = JSON.parse(body);
    posted.get(result.run_id)?.(result);
    res.writeHead(204).end();
  });
}).listen(17880, "127.0.0.1");
const receiverURL = "http://127.0.0.1:17880";

async function resultAfter(runId: string, action: () => Promise<unknown>, timeoutMs = 20000) {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const result = new Promise<Result>((resolve, reject) => {
    timer = setTimeout(() => reject(new Error(`no result for ${runId}`)), timeoutMs);
    posted.set(runId, resolve);
  });
  try {
    const [value] = await Promise.all([result, Promise.resolve().then(action)]);
    return value;
  } finally {
    clearTimeout(timer);
    posted.delete(runId);
  }
}

async function newPage(browser: Browser) {
  const context = await browser.newContext();
  await context.exposeBinding("__gmXhr", async (_source, d: GMRequest) => {
    try {
      const res = await fetch(d.url, { method: d.method, headers: d.headers, body: d.data });
      return { status: res.status, responseText: await res.text() };
    } catch {
      return null;
    }
  });
  await context.addInitScript(`
    window.__isutoriMenus = new Map();
    window.GM_registerMenuCommand = (name, callback) => window.__isutoriMenus.set(name, callback);
    window.GM_xmlhttpRequest = (d) => window.__gmXhr({ url: d.url, method: d.method, headers: d.headers, data: d.data })
      .then((r) => (r ? d.onload(r) : d.onerror()));
    document.addEventListener("DOMContentLoaded", () => { ${userscript} });
  `);
  const page = await context.newPage();
  page.on("pageerror", (e) => {
    throw e;
  });
  return page;
}

async function checkEnqueue(page: Page, target: string) {
  const messages: string[] = [];
  let prompts = 0;
  const onDialog = async (dialog: Dialog) => {
    if (dialog.type() === "prompt") {
      prompts++;
      await dialog.accept(target);
    } else {
      messages.push(dialog.message());
      await dialog.dismiss();
    }
  };
  page.on("dialog", onDialog);
  try {
    await page.evaluate(async () => {
      const command = window.__isutoriMenus.get("isutori: check enqueue");
      if (!command) throw new Error("check enqueue menu was not registered");
      await command();
    });
    return { messages, prompts };
  } finally {
    page.off("dialog", onDialog);
  }
}

const pass = startPortal(17881, "pass");
const fail = startPortal(17882, "fail");
const error = startPortal(17883, "error");
// Its jobs outlast the userscript's enqueue, so a job started by hand is still running.
const slow = startPortal(17884, "pass", "60s");
await new Promise((r) => setTimeout(r, 1000));
let browser: Browser | undefined;
try {
  browser = await chromium.launch({ headless: true });
  {
    const page = await newPage(browser);
    await page.goto(`${pass}/contestant`);
    await page.waitForLoadState("networkidle");
    const requestsBefore = receivedRequests;
    const checked = await checkEnqueue(page, "2");
    assert.equal(checked.prompts, 1);
    const result: { requested: string; selected: string; ready: boolean; enqueued: boolean } = JSON.parse(checked.messages[0]);
    assert.equal(result.requested, "2");
    assert.match(result.selected, /192\.168\.0\.12/);
    assert.equal(result.ready, true);
    assert.equal(result.enqueued, false);
    assert.equal(await page.locator("select").inputValue(), "2");
    assert.equal(await page.evaluate(() => localStorage.getItem("isutori-mock-portal-jobs")), null);
    assert.equal(await page.evaluate(() => sessionStorage.getItem("isutori-autobench")), null);
    assert.equal(receivedRequests, requestsBefore);

    const runPage = await page.context().newPage();
    const completed = await resultAfter("after-check", () =>
      runPage.goto(`${pass}/contestant#autobench=after-check&receiver=${receiverURL}&target=192.168.0.12`));
    assert.equal(completed.passed, true);
    const jobs = await runPage.evaluate(() => JSON.parse(localStorage.getItem("isutori-mock-portal-jobs") ?? "[]"));
    assert.equal(jobs.length, 1);
    assert.equal(jobs[0].targetId, 2);
    await runPage.close();
    await page.close();
    console.log("check enqueue: selects without submitting, then normal run submits once");
  }
  {
    const page = await newPage(browser);
    await page.goto(`${pass}/contestant`);
    await page.waitForLoadState("networkidle");
    const requestsBefore = receivedRequests;
    const checked = await checkEnqueue(page, "9");
    assert.match(checked.messages[0], /^prepare: target "9": target "9" matches 0/);
    assert.equal(await page.evaluate(() => localStorage.getItem("isutori-mock-portal-jobs")), null);
    assert.equal(await page.evaluate(() => sessionStorage.getItem("isutori-autobench")), null);
    assert.equal(receivedRequests, requestsBefore);
    await page.close();
    console.log("check enqueue: unknown target does not submit or create a run");
  }
  for (const attribute of ["disabled", "aria-disabled"]) {
    const page = await newPage(browser);
    await page.clock.install();
    await page.goto(`${pass}/contestant`);
    await page.waitForLoadState("networkidle");
    await page.getByRole("button", { name: "Enqueue" }).evaluate((button, name) => button.setAttribute(name, "true"), attribute);
    const requestsBefore = receivedRequests;
    const checking = checkEnqueue(page, "2");
    await page.waitForFunction(() => document.querySelector("select")?.value === "2");
    await page.clock.fastForward(61000);
    const checked = await checking;
    assert.match(checked.messages[0], /waiting for the enabled Enqueue button/);
    assert.equal(await page.evaluate(() => localStorage.getItem("isutori-mock-portal-jobs")), null);
    assert.equal(receivedRequests, requestsBefore);
    await page.getByRole("button", { name: "Enqueue" }).evaluate((button, name) => button.removeAttribute(name), attribute);
    const retried = await checkEnqueue(page, "2");
    assert.equal(JSON.parse(retried.messages[0]).ready, true);
    assert.equal(await page.evaluate(() => localStorage.getItem("isutori-mock-portal-jobs")), null);
    await page.close();
    console.log(`check enqueue: ${attribute} blocks submission and permits retry after enabling`);
  }
  {
    const page = await newPage(browser);
    await page.goto(`${slow}/contestant#autobench=menu-active&receiver=${receiverURL}&target=1`);
    await page.waitForURL(/benchmark_jobs\/\d+$/);
    await page.waitForFunction(() => JSON.parse(sessionStorage.getItem("isutori-autobench") ?? "null")?.phase === "waiting");
    for (const phase of ["waiting", "unreported"]) {
      if (phase === "unreported") {
        await page.evaluate(() => {
          const state = JSON.parse(sessionStorage.getItem("isutori-autobench") ?? "null");
          state.phase = "unreported";
          sessionStorage.setItem("isutori-autobench", JSON.stringify(state));
        });
      }
      const before = await page.evaluate(() => sessionStorage.getItem("isutori-autobench"));
      const checked = await checkEnqueue(page, "2");
      assert.equal(checked.prompts, 0);
      assert.match(checked.messages[0], /Open a separate portal tab/);
      assert.equal(await page.evaluate(() => sessionStorage.getItem("isutori-autobench")), before);
      assert.equal(await page.evaluate(() => JSON.parse(localStorage.getItem("isutori-mock-portal-jobs") ?? "[]").length), 1);
    }
    await page.close();
    console.log("check enqueue: protects active and unreported runs");
  }
  {
    const page = await newPage(browser);
    const r = await resultAfter("pass", () => page.goto(`${pass}/contestant#autobench=pass&receiver=${receiverURL}&target=2`));
    assert.equal(r.passed, true);
    const jobs = await page.evaluate(() => JSON.parse(localStorage.getItem("isutori-mock-portal-jobs") ?? "[]"));
    assert.equal(jobs[0].targetId, 2);
    assert.equal(r.score, 12345);
    assert.match(r.message ?? "", /score=12345/);
    assert.equal(r.error, undefined);

    const again = await resultAfter("pass-again", () => page.goto(`${pass}/contestant#autobench=pass-again&receiver=${receiverURL}&target=192.168.0.12`));
    assert.equal(again.passed, true);
    assert.match(page.url(), /benchmark_jobs\/2$/);
  }
  {
    const page = await newPage(browser);
    const r = await resultAfter("fail", () => page.goto(`${fail}/contestant#autobench=fail&receiver=${receiverURL}&target=1`));
    assert.equal(r.passed, false);
    assert.equal(r.score, 0);
    assert.match(r.message ?? "", /mock portal was configured to fail/);
  }
  {
    const page = await newPage(browser);
    const r = await resultAfter("errored", () => page.goto(`${error}/contestant#autobench=errored&receiver=${receiverURL}&target=1`));
    assert.equal(r.passed, false);
    assert.match(r.message ?? "", /^ERRORED/);
  }
  {
    const page = await newPage(browser);
    const result = await resultAfter("reload", async () => {
      await page.goto(`${pass}/contestant#autobench=reload&receiver=${receiverURL}&target=1`);
      await page.waitForURL(/benchmark_jobs\/\d+$/);
      await page.reload();
    });
    assert.equal(result.passed, true);
    assert.match(page.url(), /benchmark_jobs\/1$/);
  }
  {
    const page = await newPage(browser);
    await page.goto(`${slow}/contestant`);
    await page.locator("select").selectOption("1");
    await page.getByRole("button", { name: "Enqueue" }).click();
    await page.waitForURL(/benchmark_jobs\/\d+$/);
    const r = await resultAfter("busy", () => page.goto(`${slow}/contestant#autobench=busy&receiver=${receiverURL}&target=1`));
    assert.match(r.error ?? "", /^enqueue: portal refused to enqueue: .*no other concurrent job/);
  }
  {
    const page = await newPage(browser);
    const result = await resultAfter("unknown-target", () => page.goto(`${pass}/contestant#autobench=unknown-target&receiver=${receiverURL}&target=9`));
    assert.match(result.error ?? "", /^enqueue: target "9" matches 0 of: 1: 192/);
    assert.equal(await page.evaluate(() => localStorage.getItem("isutori-mock-portal-jobs")), null);
  }
  console.log("userscript: OK");
} finally {
  await browser?.close();
  receiver.close();
  portals.forEach((p) => p.kill());
}
