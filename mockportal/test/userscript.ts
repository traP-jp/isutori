// Runs the userscript (with its example adapter) against `isutori mock-portal`
// and checks what it reports to a stand-in for the isutori receiver.
import assert from "node:assert/strict";
import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { createServer } from "node:http";
import { chromium, type Browser } from "playwright";

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
function startPortal(port: number, outcome: "pass" | "fail" | "error") {
  portals.push(
    spawn(".cache/isutori", ["mock-portal", "--listen", `127.0.0.1:${port}`, "--pending", "1s", "--running", "2s", "--outcome", outcome], {
      stdio: "ignore",
    }),
  );
  return `http://127.0.0.1:${port}`;
}

const posted = new Map<string, (r: Result) => void>();
const receiver = createServer((req, res) => {
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    const result: Result = JSON.parse(body);
    posted.get(result.run_id)?.(result);
    res.writeHead(204).end();
  });
}).listen(17880, "127.0.0.1");
const receiverURL = "http://127.0.0.1:17880";

function waitForResult(runId: string, timeoutMs = 20000) {
  return new Promise<Result>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`no result for ${runId}`)), timeoutMs);
    posted.set(runId, (r) => {
      clearTimeout(timer);
      resolve(r);
    });
  });
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
    window.GM_registerMenuCommand = () => {};
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

const pass = startPortal(17881, "pass");
const fail = startPortal(17882, "fail");
const error = startPortal(17883, "error");
await new Promise((r) => setTimeout(r, 1000));
const browser = await chromium.launch();
try {
  {
    const page = await newPage(browser);
    const result = waitForResult("pass");
    await page.goto(`${pass}/contestant#autobench=pass&receiver=${receiverURL}&target=2`);
    const r = await result;
    assert.equal(r.passed, true);
    const jobs = await page.evaluate(() => JSON.parse(localStorage.getItem("isutori-mock-portal-jobs") ?? "[]"));
    assert.equal(jobs[0].targetId, 2);
    assert.equal(r.score, 12345);
    assert.match(r.message ?? "", /score=12345/);
    assert.equal(r.error, undefined);

    // A second run in the same tab enqueues a new job instead of resuming.
    const again = waitForResult("pass-again");
    await page.goto(`${pass}/contestant#autobench=pass-again&receiver=${receiverURL}&target=192.168.0.12`);
    assert.equal((await again).passed, true);
    assert.match(page.url(), /benchmark_jobs\/2$/);
  }
  {
    const page = await newPage(browser);
    const result = waitForResult("fail");
    await page.goto(`${fail}/contestant#autobench=fail&receiver=${receiverURL}&target=1`);
    const r = await result;
    assert.equal(r.passed, false);
    assert.equal(r.score, 0);
    assert.match(r.message ?? "", /mock portal was configured to fail/);
  }
  {
    const page = await newPage(browser);
    const result = waitForResult("errored");
    await page.goto(`${error}/contestant#autobench=errored&receiver=${receiverURL}&target=1`);
    const r = await result;
    assert.equal(r.passed, false);
    assert.match(r.message ?? "", /^ERRORED/);
  }
  {
    // Reloading the job page while waiting resumes instead of enqueuing again.
    const page = await newPage(browser);
    const result = waitForResult("reload");
    await page.goto(`${pass}/contestant#autobench=reload&receiver=${receiverURL}&target=1`);
    await page.waitForURL(/benchmark_jobs\/\d+$/);
    await page.reload();
    assert.equal((await result).passed, true);
    assert.match(page.url(), /benchmark_jobs\/1$/);
  }
  {
    // A job started by hand makes the portal refuse; this must surface as an error.
    const page = await newPage(browser);
    await page.goto(`${pass}/contestant`);
    await page.locator("select").selectOption("1");
    await page.getByRole("button", { name: "Enqueue" }).click();
    await page.waitForURL(/benchmark_jobs\/\d+$/);
    const result = waitForResult("busy");
    await page.goto(`${pass}/contestant#autobench=busy&receiver=${receiverURL}&target=1`);
    const r = await result;
    assert.match(r.error ?? "", /^enqueue: portal refused to enqueue: .*no other concurrent job/);
  }
  {
    // An unknown target must not fall back to another server.
    const page = await newPage(browser);
    const result = waitForResult("unknown-target");
    await page.goto(`${pass}/contestant#autobench=unknown-target&receiver=${receiverURL}&target=9`);
    assert.match((await result).error ?? "", /^enqueue: bench.target "9" matches 0 of: 1: 192/);
    assert.equal(await page.evaluate(() => localStorage.getItem("isutori-mock-portal-jobs")), null);
  }
  console.log("userscript: OK");
} finally {
  await browser.close();
  receiver.close();
  portals.forEach((p) => p.kill());
}
