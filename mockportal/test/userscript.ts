import assert from "node:assert/strict";
import { type ChildProcess, execFileSync, spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { type Browser, chromium, type Dialog, type Page } from "playwright";

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
    spawn(
      ".cache/isutori",
      ["mock-portal", "--listen", `127.0.0.1:${port}`, "--pending", "1s", "--running", running, "--outcome", outcome],
      {
        stdio: "ignore",
      },
    ),
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
    if (req.url === "/api/userscript") {
      const request: { source: string } = JSON.parse(body);
      const actual = `sha256:${createHash("sha256").update(request.source.replace(/\r\n?/g, "\n")).digest("hex")}`;
      const source = userscript.slice(
        userscript.indexOf("function isutoriScript() {"),
        userscript.lastIndexOf("})();") + 1,
      );
      const expected = `sha256:${createHash("sha256").update(source.replace(/\r\n?/g, "\n")).digest("hex")}`;
      res
        .writeHead(200, { "Content-Type": "application/json" })
        .end(JSON.stringify({ expected, actual, status: actual === expected ? "matched" : "mismatched" }));
      return;
    }
    if (req.url === "/api/heartbeat") {
      res.writeHead(204).end();
      return;
    }
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

async function newPage(browser: Browser, script = userscript) {
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
    document.addEventListener("DOMContentLoaded", () => { ${script} });
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
    const runId = "final-result-transitions";
    await page.context().route(`${pass}/contestant/benchmark_jobs/77`, (route) =>
      route.fulfill({
        contentType: "text/html",
        body: `<div class="card"><h4 class="card-header-title">Summary</h4><p>ID: <span id="job-id">76</span></p><span class="tag" id="job-status">FINISHED</span></div>
        <div class="card"><h4 class="card-header-title">Result</h4><span class="tag" id="result-status">Finished</span><span class="tag" id="verdict">Passed</span><p>Score: <span id="score">99999</span></p></div>
        <div class="card"><h4 class="card-header-title">Conclusion</h4><div class="card-content">final result</div></div>`,
      }),
    );
    await page.context().addInitScript(
      ({ runId, receiverURL }) => {
        sessionStorage.setItem(
          "isutori-autobench-v2",
          JSON.stringify({
            runId,
            receiver: receiverURL,
            target: "2",
            phase: "waiting",
            job: { jobId: "77" },
            startedAt: Date.now(),
          }),
        );
      },
      { runId, receiverURL },
    );
    let reported = false;
    const set = async (values: Record<string, string>) =>
      page.evaluate((values) => {
        for (const [id, value] of Object.entries(values)) {
          const element = document.getElementById(id);
          if (!element) throw new Error(`missing fixture element: ${id}`);
          element.textContent = value;
        }
      }, values);
    const waiting = async () => {
      await page.waitForTimeout(2200);
      assert.equal(reported, false, "reported a stale or unfinished score");
    };
    const result = await resultAfter(
      runId,
      async () => {
        const onResult = posted.get(runId);
        assert.ok(onResult);
        posted.set(runId, (result) => {
          reported = true;
          onResult(result);
        });
        await page.goto(`${pass}/contestant/benchmark_jobs/77`);
        await waiting();
        await set({ "job-id": "77", "job-status": "RUNNING", "result-status": "In progress", score: "100" });
        await waiting();
        await waiting();
        await set({ score: "150" });
        await waiting();
        await set({ "job-status": "FINISHED" });
        await waiting();
        await set({ "result-status": "Finished", verdict: "", score: "" });
        await waiting();
        await set({ verdict: "Passed", score: "789" });
      },
      40000,
    );
    assert.equal(result.passed, true);
    assert.equal(result.score, 789);
    await page.close();
    console.log("final result: ignores old jobs, growing/paused scores and incomplete final fields");
  }
  for (const manual of [false, true]) {
    const runId = manual ? "identity-manual" : "identity-integration";
    const work = mkdtempSync(join(tmpdir(), "isutori-identity-"));
    const scriptPath = join(work, "script.user.js");
    const resultPath = join(work, "bench.json");
    writeFileSync(scriptPath, userscript);
    const cli = spawn(
      resolve(".cache/isutori"),
      [
        "autobench",
        "--listen",
        "127.0.0.1:17885",
        "--userscript",
        scriptPath,
        "--open-command",
        "/bin/true",
        `${pass}/contestant`,
        "2",
      ],
      {
        env: { ...process.env, ISUTORI_RUN_ID: runId, ISUTORI_BENCH_RESULT: resultPath },
        stdio: ["ignore", "ignore", "pipe"],
      },
    );
    let output = "";
    cli.stderr.on("data", (chunk) => {
      output += chunk;
    });
    const exited = new Promise<number | null>((resolve) => cli.once("close", resolve));
    const page = await newPage(
      browser,
      manual ? userscript.replace("async prepare(target)", "async prepare(target /* old */)") : userscript,
    );
    try {
      await new Promise<void>((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error(`CLI did not start: ${output}`)), 10000);
        const ready = () => {
          if (output.includes("waiting for a verified userscript")) {
            clearTimeout(timer);
            cli.stderr.off("data", ready);
            resolve();
          }
        };
        cli.stderr.on("data", ready);
        ready();
        exited.then(() => {
          clearTimeout(timer);
          reject(new Error(`CLI exited: ${output}`));
        });
      });
      await page.goto(`${pass}/contestant#autobench_v2=${runId}&receiver=http://127.0.0.1:17885&target=2`);
      if (manual) {
        await page.getByText(/BLOCKED: userscript verification/).waitFor();
        assert.equal(await page.evaluate(() => localStorage.getItem("isutori-mock-portal-jobs")), null);
        const form = await page.context().newPage();
        await form.goto("http://127.0.0.1:17885/");
        await form.locator('[name="passed"]').check();
        await form.locator('[name="score"]').fill("42");
        await form.getByRole("button", { name: "送信", exact: true }).click();
        await form
          .locator("#status")
          .getByText("送信しました。このタブは閉じて構いません。", { exact: true })
          .waitFor();
        await form.close();
      }
      const code = await Promise.race([
        exited,
        new Promise<never>((_, reject) => {
          const timer = setTimeout(() => reject(new Error(`CLI did not finish: ${output}`)), 20000);
          exited.finally(() => clearTimeout(timer));
        }),
      ]);
      assert.equal(code, 0, output);
      const result: {
        passed: boolean;
        score: number;
        userscript: { status: string; expected: string; actual: string };
      } = JSON.parse(readFileSync(resultPath, "utf8"));
      assert.equal(result.passed, true);
      assert.equal(result.score, manual ? 42 : 12345);
      assert.equal(result.userscript.status, manual ? "not_checked" : "matched");
      assert.equal(result.userscript.expected, result.userscript.actual);
      console.log(
        `userscript identity: real autobench CLI saved ${manual ? "manual result after mismatch" : "verified benchmark result"}`,
      );
    } finally {
      await page.close();
      cli.kill("SIGINT");
      await exited;
    }
  }
  {
    const page = await newPage(browser, userscript.replace("async prepare(target)", "async prepare(target /* old */)"));
    await page.goto(`${pass}/contestant#autobench_v2=blocked&receiver=${receiverURL}&target=2`);
    await page.getByText(/BLOCKED: userscript verification/).waitFor();
    assert.equal(await page.evaluate(() => localStorage.getItem("isutori-mock-portal-jobs")), null);
    assert.equal(await page.locator("select").inputValue(), "");
    await page.reload();
    await page.getByText(/BLOCKED: userscript verification/).waitFor();
    assert.equal(await page.evaluate(() => localStorage.getItem("isutori-mock-portal-jobs")), null);
    await page.close();
    console.log("userscript identity: mismatch blocks preparation before and after reload");
  }
  {
    const page = await newPage(browser);
    await page.goto(`${pass}/contestant`);
    await page.waitForLoadState("networkidle");
    const requestsBefore = receivedRequests;
    const checked = await checkEnqueue(page, "2");
    assert.equal(checked.prompts, 1);
    const result: { requested: string; selected: string; ready: boolean; enqueued: boolean } = JSON.parse(
      checked.messages[0],
    );
    assert.equal(result.requested, "2");
    assert.match(result.selected, /192\.168\.0\.12/);
    assert.equal(result.ready, true);
    assert.equal(result.enqueued, false);
    assert.equal(await page.locator("select").inputValue(), "2");
    assert.equal(await page.evaluate(() => localStorage.getItem("isutori-mock-portal-jobs")), null);
    assert.equal(await page.evaluate(() => sessionStorage.getItem("isutori-autobench-v2")), null);
    assert.equal(receivedRequests, requestsBefore);

    const runPage = await page.context().newPage();
    const completed = await resultAfter("after-check", () =>
      runPage.goto(`${pass}/contestant#autobench_v2=after-check&receiver=${receiverURL}&target=192.168.0.12`),
    );
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
    assert.equal(await page.evaluate(() => sessionStorage.getItem("isutori-autobench-v2")), null);
    assert.equal(receivedRequests, requestsBefore);
    await page.close();
    console.log("check enqueue: unknown target does not submit or create a run");
  }
  for (const attribute of ["disabled", "aria-disabled"]) {
    const page = await newPage(browser);
    await page.clock.install();
    await page.goto(`${pass}/contestant`);
    await page.waitForLoadState("networkidle");
    await page
      .getByRole("button", { name: "Enqueue" })
      .evaluate((button, name) => button.setAttribute(name, "true"), attribute);
    const requestsBefore = receivedRequests;
    const checking = checkEnqueue(page, "2");
    await page.waitForFunction(() => document.querySelector("select")?.value === "2");
    await page.clock.fastForward(61000);
    const checked = await checking;
    assert.match(checked.messages[0], /waiting for the enabled Enqueue button/);
    assert.equal(await page.evaluate(() => localStorage.getItem("isutori-mock-portal-jobs")), null);
    assert.equal(receivedRequests, requestsBefore);
    await page
      .getByRole("button", { name: "Enqueue" })
      .evaluate((button, name) => button.removeAttribute(name), attribute);
    const retried = await checkEnqueue(page, "2");
    assert.equal(JSON.parse(retried.messages[0]).ready, true);
    assert.equal(await page.evaluate(() => localStorage.getItem("isutori-mock-portal-jobs")), null);
    await page.close();
    console.log(`check enqueue: ${attribute} blocks submission and permits retry after enabling`);
  }
  {
    const page = await newPage(browser);
    await page.goto(`${slow}/contestant#autobench_v2=menu-active&receiver=${receiverURL}&target=1`);
    await page.waitForURL(/benchmark_jobs\/\d+$/);
    await page.waitForFunction(
      () => JSON.parse(sessionStorage.getItem("isutori-autobench-v2") ?? "null")?.phase === "waiting",
    );
    for (const phase of ["waiting", "unreported"]) {
      if (phase === "unreported") {
        await page.evaluate(() => {
          const state = JSON.parse(sessionStorage.getItem("isutori-autobench-v2") ?? "null");
          state.phase = "unreported";
          sessionStorage.setItem("isutori-autobench-v2", JSON.stringify(state));
        });
      }
      const before = await page.evaluate(() => sessionStorage.getItem("isutori-autobench-v2"));
      const checked = await checkEnqueue(page, "2");
      assert.equal(checked.prompts, 0);
      assert.match(checked.messages[0], /Open a separate portal tab/);
      assert.equal(await page.evaluate(() => sessionStorage.getItem("isutori-autobench-v2")), before);
      assert.equal(
        await page.evaluate(() => JSON.parse(localStorage.getItem("isutori-mock-portal-jobs") ?? "[]").length),
        1,
      );
    }
    await page.close();
    console.log("check enqueue: protects active and unreported runs");
  }
  {
    const page = await newPage(browser);
    const r = await resultAfter("pass", () =>
      page.goto(`${pass}/contestant#autobench_v2=pass&receiver=${receiverURL}&target=2`),
    );
    assert.equal(r.passed, true);
    const jobs = await page.evaluate(() => JSON.parse(localStorage.getItem("isutori-mock-portal-jobs") ?? "[]"));
    assert.equal(jobs[0].targetId, 2);
    assert.equal(r.score, 12345);
    assert.match(r.message ?? "", /score=12345/);
    assert.equal(r.error, undefined);

    const again = await resultAfter("pass-again", () =>
      page.goto(`${pass}/contestant#autobench_v2=pass-again&receiver=${receiverURL}&target=192.168.0.12`),
    );
    assert.equal(again.passed, true);
    assert.match(page.url(), /benchmark_jobs\/2$/);
  }
  {
    const page = await newPage(browser);
    const r = await resultAfter("fail", () =>
      page.goto(`${fail}/contestant#autobench_v2=fail&receiver=${receiverURL}&target=1`),
    );
    assert.equal(r.passed, false);
    assert.equal(r.score, 0);
    assert.match(r.message ?? "", /mock portal was configured to fail/);
  }
  {
    const page = await newPage(browser);
    const r = await resultAfter("errored", () =>
      page.goto(`${error}/contestant#autobench_v2=errored&receiver=${receiverURL}&target=1`),
    );
    assert.equal(r.passed, false);
    assert.match(r.message ?? "", /^ERRORED/);
  }
  {
    const page = await newPage(browser);
    const result = await resultAfter("reload", async () => {
      await page.goto(`${pass}/contestant#autobench_v2=reload&receiver=${receiverURL}&target=1`);
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
    const r = await resultAfter("busy", () =>
      page.goto(`${slow}/contestant#autobench_v2=busy&receiver=${receiverURL}&target=1`),
    );
    assert.match(r.error ?? "", /^enqueue: portal refused to enqueue: .*no other concurrent job/);
  }
  {
    const page = await newPage(browser);
    const result = await resultAfter("unknown-target", () =>
      page.goto(`${pass}/contestant#autobench_v2=unknown-target&receiver=${receiverURL}&target=9`),
    );
    assert.match(result.error ?? "", /^enqueue: target "9" matches 0 of: 1: 192/);
    assert.equal(await page.evaluate(() => localStorage.getItem("isutori-mock-portal-jobs")), null);
  }
  console.log("userscript: OK");
} finally {
  await browser?.close();
  receiver.close();
  for (const p of portals) p.kill();
}
