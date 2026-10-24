import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { chromium, type Page } from "playwright";

const source = readFileSync(new URL("../../internal/userscript/isutori.user.js", import.meta.url), "utf8");
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext();
const values: Record<string, unknown> = {};
const tabData = new Map<Page, object>();
const group = "isutori-tabs:http://portal.example:http://localhost:7878";
const pages: Page[] = [];
const fixture = `<select><option value="2">2: 192.168.0.12</option></select><button onclick="history.replaceState(null,'','/contestant/benchmark_jobs/1')">Enqueue</button>
<div class="card"><h4 class="card-header-title">Summary</h4><p>ID: 1</p><span class="tag" id="status">RUNNING</span></div>
<div class="card"><h4 class="card-header-title">Result</h4><span class="tag" id="finished">In progress</span><span class="tag" id="verdict"></span><p>Score: <span id="score">10</span></p></div>`;
await context.route("http://portal.example/**", (route) => route.fulfill({ contentType: "text/html", body: fixture }));
await context.exposeBinding("tabRead", ({ page }) => ({
  tab: tabData.get(page) ?? {},
  values: structuredClone(values),
}));
await context.exposeBinding("tabSave", ({ page }, data: object) => {
  tabData.set(page, data);
});
await context.exposeBinding("valueSet", async ({ page: sender }, key: string, value: unknown) => {
  values[key] = value;
  await Promise.all(
    pages
      .filter((page) => page !== sender && !page.isClosed())
      .map((page) =>
        page.evaluate(
          ({ key, value }) => {
            const old = window.tabValues[key];
            window.tabValues[key] = value;
            for (const listener of window.tabListeners.get(key) ?? []) listener(key, old, value, true);
          },
          { key, value },
        ),
      ),
  );
});

declare global {
  interface Window {
    tabValues: Record<string, unknown>;
    tabListeners: Map<string, ((key: string, old: unknown, value: unknown, remote: boolean) => void)[]>;
    tabMenus: Map<string, () => void>;
    closeCalls: number;
    requests: { url: string; data: string }[];
    failResult: boolean;
    failClose: boolean;
    failInspect: boolean;
    tabRead: () => Promise<{ tab: object; values: Record<string, unknown> }>;
    tabSave: (data: object) => Promise<void>;
    valueSet: (key: string, value: unknown) => Promise<void>;
  }
}

async function open(runId?: string, receiver = "http://localhost:7878", copiedState?: string) {
  const page = await context.newPage();
  pages.push(page);
  await page.addInitScript(
    ({ values, copiedState }) => {
      window.tabValues = values;
      window.tabListeners = new Map();
      window.tabMenus = new Map();
      window.closeCalls = 0;
      window.requests = [];
      window.failResult = false;
      window.failClose = false;
      window.failInspect = false;
      if (copiedState) sessionStorage.setItem("isutori-autobench-v2", copiedState);
      Object.assign(window, {
        GM_getValue: (key: string, fallback: unknown) => window.tabValues[key] ?? fallback,
        GM_setValue: (key: string, value: unknown) => {
          window.tabValues[key] = value;
          void window.valueSet(key, value);
        },
        GM_addValueChangeListener: (
          key: string,
          callback: (key: string, old: unknown, value: unknown, remote: boolean) => void,
        ) => {
          const list = window.tabListeners.get(key) ?? [];
          list.push(callback);
          window.tabListeners.set(key, list);
        },
        GM_getTab: (callback: (data: object) => void) => {
          void window.tabRead().then((data) => {
            window.tabValues = data.values;
            callback(data.tab);
          });
        },
        GM_saveTab: (data: object) => {
          void window.tabSave(data);
        },
        GM_registerMenuCommand: (name: string, callback: () => void) => {
          window.tabMenus.set(name, callback);
        },
        GM_xmlhttpRequest: (request: {
          url: string;
          data: string;
          onload: (response: { status: number; responseText: string }) => void;
          onerror: () => void;
        }) => {
          window.requests.push({ url: request.url, data: request.data });
          if (request.url.includes("/api/inspect")) {
            if (window.failInspect) return request.onerror();
            const verification = { status: "matched", expected: "id", actual: "id" };
            return request.onload({
              status: 200,
              responseText: JSON.stringify(
                request.url.endsWith("/start") ? { origin: location.origin, userscript: verification } : verification,
              ),
            });
          }
          if (request.url.endsWith("/api/userscript"))
            request.onload({
              status: 200,
              responseText: JSON.stringify({ status: "matched", expected: "id", actual: "id" }),
            });
          else if (window.failResult) request.onerror();
          else request.onload({ status: 204, responseText: "" });
        },
      });
      const timeout = window.setTimeout.bind(window);
      Object.assign(window, {
        setTimeout: (callback: TimerHandler, delay?: number, ...args: unknown[]) =>
          timeout(callback, delay === 2000 ? 20 : delay, ...args),
      });
      window.alert = () => {};
      window.prompt = () => "http://localhost:7877/#abcdefghijklmnopqrstuvwx";
      window.close = () => {
        window.closeCalls++;
        if (window.failClose) throw new Error("close denied");
      };
    },
    { values: structuredClone(values), copiedState },
  );
  await page.goto(
    `http://portal.example/contestant${runId ? "#" + new URLSearchParams({ autobench_v2: runId, receiver, target: "2" }) : ""}`,
  );
  await page.evaluate(source);
  if (runId)
    await page.waitForFunction(
      () => JSON.parse(sessionStorage.getItem("isutori-autobench-v2") ?? "null")?.phase === "waiting",
    );
  return page;
}
async function menu(page: Page, name: string) {
  await page.evaluate((name) => window.tabMenus.get(name)?.(), name);
}
async function finish(page: Page, passed = true) {
  await page.evaluate((passed) => {
    document.getElementById("status")!.textContent = "FINISHED";
    document.getElementById("finished")!.textContent = "Finished";
    document.getElementById("verdict")!.textContent = passed ? "Passed" : "Failed";
    document.getElementById("score")!.textContent = passed ? "42" : "0";
  }, passed);
  await page.waitForFunction(() =>
    ["done", "unreported"].includes(JSON.parse(sessionStorage.getItem("isutori-autobench-v2") ?? "null")?.phase),
  );
  await page.waitForTimeout(80);
}
async function kept(page: Page) {
  assert.equal(await page.evaluate(() => window.closeCalls), 0);
}
try {
  const first = await open("first");
  await finish(first);
  const second = await open("second");
  await finish(second);
  await kept(first);
  await kept(second);
  await menu(second, "isutori: toggle automatic tab cleanup");
  await first.waitForFunction(() => window.closeCalls > 0);
  await kept(second);

  const copied = await open(
    undefined,
    undefined,
    await second.evaluate(() => sessionStorage.getItem("isutori-autobench-v2") ?? ""),
  );
  const manual = await open();
  await menu(second, "isutori: toggle keep this tab");
  const third = await open("third");
  await finish(third, false);
  await kept(second);
  await kept(third);
  await kept(copied);
  await kept(manual);
  await menu(second, "isutori: toggle keep this tab");
  await second.waitForFunction(() => window.closeCalls > 0);

  const failed = await open("failed");
  await menu(failed, "isutori: toggle keep this tab");
  await failed.evaluate(() => {
    window.failResult = true;
  });
  await finish(failed);
  assert.equal(
    await failed.evaluate(() => JSON.parse(sessionStorage.getItem("isutori-autobench-v2") ?? "null").tab.keep),
    true,
  );
  await failed.reload();
  await failed.evaluate(source);
  await failed.waitForFunction(() => window.tabMenus.has("isutori: toggle keep this tab"));
  assert.equal(
    await failed.evaluate(() => JSON.parse(sessionStorage.getItem("isutori-autobench-v2") ?? "null").tab.keep),
    true,
  );
  await failed.waitForTimeout(80);
  await menu(failed, "isutori: toggle keep this tab");
  assert.equal(
    await failed.evaluate(() => JSON.parse(sessionStorage.getItem("isutori-autobench-v2") ?? "null").tab.keep),
    false,
  );
  const error = await open("error");
  await error.evaluate(() => {
    document.getElementById("status")!.textContent = "UNKNOWN";
  });
  await error.waitForFunction(
    () => JSON.parse(sessionStorage.getItem("isutori-autobench-v2") ?? "null")?.phase === "done",
  );
  const changed = await open("changed");
  await finish(changed);
  await changed.evaluate(() => history.replaceState(null, "", "/contestant/benchmark_jobs/2"));
  const other = await open("other", "http://localhost:9999");
  await finish(other);
  const recording = await open("recording");
  await finish(recording);
  await menu(recording, "isutori: start recording");
  await recording.waitForFunction(() =>
    document.querySelector("[data-isutori-inspect]")?.textContent?.includes("recording; matched"),
  );
  const unsaved = await open("unsaved");
  await finish(unsaved);
  await menu(unsaved, "isutori: start recording");
  await unsaved.evaluate(() => {
    window.failInspect = true;
    document.body.append(document.createElement("hr"));
  });
  await unsaved.waitForFunction(() =>
    document.querySelector("[data-isutori-inspect]")?.textContent?.includes("unsaved event"),
  );
  const latest = await open("latest");
  await finish(latest);
  await kept(recording);
  await kept(unsaved);
  await menu(recording, "isutori: stop recording");
  await recording.waitForFunction(() => window.closeCalls > 0);
  await kept(failed);
  await kept(error);
  await kept(changed);
  await kept(other);
  await kept(latest);
  await kept(copied);

  const current = structuredClone(values[group]);
  await latest.evaluate(
    ({ group }) => {
      for (const listener of window.tabListeners.get(group) ?? [])
        listener(group, null, { runId: "old", order: 1, receivedOrder: 1 }, true);
    },
    { group },
  );
  await kept(latest);
  assert.deepEqual(values[group], current);

  const missed = await open("missed");
  await finish(missed);
  await missed.evaluate(() => {
    window.tabListeners.clear();
  });
  const newer = await open("newer");
  await finish(newer);
  await kept(missed);
  const beforeReload = structuredClone(values[group]);
  await missed.reload();
  await missed.evaluate(source);
  await missed.waitForFunction(() => window.closeCalls > 0);
  assert.deepEqual(values[group], beforeReload);

  const denied = await open("denied");
  await finish(denied);
  await denied.evaluate(() => {
    window.failClose = true;
  });
  const final = await open("final");
  await menu(final, "isutori: toggle keep this tab");
  await finish(final);
  await denied.waitForFunction(() => window.closeCalls > 0);
  assert.equal(
    await final.evaluate(() => JSON.parse(sessionStorage.getItem("isutori-autobench-v2") ?? "null").phase),
    "done",
  );
  await kept(final);
  const late = await open("late");
  const fresh = await open("fresh");
  await kept(final);
  await finish(fresh);
  await kept(final);
  await kept(late);
  await finish(late);
  await late.waitForFunction(() => window.closeCalls > 0);
  await kept(fresh);
  assert.match(JSON.stringify(values[group]), /"runId":"fresh"/);
  const unavailable = await open();
  await unavailable.evaluate(() => {
    Object.assign(window, {
      GM_getValue: () => {
        throw new Error("storage unavailable");
      },
    });
    history.replaceState(null, "", "/contestant#autobench_v2=unavailable&receiver=http://localhost:7878&target=2");
  });
  await unavailable.evaluate(source);
  await unavailable.waitForFunction(
    () => JSON.parse(sessionStorage.getItem("isutori-autobench-v2") ?? "null")?.phase === "waiting",
  );
  await finish(unavailable);
  await kept(unavailable);
  console.log(
    "tab cleanup: OK (default off, receive-before-close, keep, manual/copied tabs, errors, grouping, stale notifications, reload, close failure)",
  );
} finally {
  await browser.close();
}
