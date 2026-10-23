import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { setImmediate } from "node:timers/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";

const source = readFileSync(new URL("../../internal/userscript/isutori.user.js", import.meta.url), "utf8");
const storageKey = "isutori-autobench";
const options = [
  { value: "1", text: "1: 192.168.0.11", disabled: false },
  { value: "2", text: "2: 192.168.0.12", disabled: false },
];

function page(initialState?: object, automatic = false) {
  const menus = new Map<string, () => void | Promise<void>>();
  const storage = new Map<string, string>();
  if (initialState) storage.set(storageKey, JSON.stringify(initialState));
  const alerts: string[] = [];
  const posted: string[] = [];
  const phases: string[] = [];
  const location = {
    pathname: "/contestant",
    search: "",
    hash: automatic ? "#autobench=test&receiver=http://localhost:7878&target=2" : "",
  };
  const controls = {
    target: "2",
    options: options.map((option) => ({ ...option })),
    value: "1",
    selected: "1",
    selectDisabled: false,
    buttonDisabled: false,
    ariaDisabled: false,
    buttonPresent: true,
    ignoreSelection: false,
    clicks: 0,
    prompts: 0,
    ticks: 0,
    onTimer: () => {},
    beforeSubmit: () => {},
  };
  const select = {
    get options() { return controls.options; },
    get value() { return controls.value; },
    set value(value: string) { controls.value = value; },
    get selectedOptions() { return controls.options.filter((option) => option.value === controls.value); },
    matches: () => controls.selectDisabled,
    dispatchEvent() {
      if (controls.ignoreSelection) controls.value = "1";
      controls.selected = controls.value;
    },
  };
  const button = {
    textContent: "Enqueue",
    matches: () => controls.buttonDisabled,
    getAttribute: () => controls.ariaDisabled ? "true" : null,
    click() { controls.clicks++; location.pathname = "/contestant/benchmark_jobs/1"; },
  };
  const cards = ["Summary", "Conclusion", "Result"].map((title) => ({
    innerText: "Score: 42",
    querySelector(selector: string) {
      if (selector === ".card-header-title") return { textContent: title };
      if (selector === ".tag") return { textContent: "FINISHED" };
      return { innerText: "official result" };
    },
    querySelectorAll() { return [{ textContent: "Finished" }, { textContent: "Passed" }]; },
  }));
  const document = {
    querySelector(selector: string) { return selector === "select" ? select : null; },
    querySelectorAll(selector: string) {
      if (selector === "button") return controls.buttonPresent ? [button] : [];
      if (selector === ".card") return cards;
      return [];
    },
    createElement() { return { style: {}, textContent: "", setAttribute() {} }; },
    body: { append() {} },
  };
  let now = 0;
  const sandbox = {
    document, location, URLSearchParams, Event,
    Date: { now: () => now },
    history: { replaceState() { location.hash = ""; } },
    sessionStorage: {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem(key: string, value: string) {
        storage.set(key, value);
        const state: { phase: string } = JSON.parse(value);
        phases.push(state.phase);
        if (state.phase === "enqueuing") controls.beforeSubmit();
      },
    },
    setTimeout(callback: () => void, delay: number) {
      queueMicrotask(() => {
        now += delay;
        controls.ticks++;
        controls.onTimer();
        callback();
      });
    },
    prompt() { controls.prompts++; return controls.target; },
    alert: (value: string) => alerts.push(value),
    GM_registerMenuCommand: (name: string, callback: () => void | Promise<void>) => menus.set(name, callback),
    GM_xmlhttpRequest(request: { data: string; onload: (response: { status: number }) => void }) {
      posted.push(request.data);
      request.onload({ status: 204 });
    },
  };
  return {
    controls, alerts, posted, phases, storage, location,
    start() { runInNewContext(source, sandbox); },
    async check() {
      const callback = menus.get("isutori: check enqueue");
      assert.ok(callback);
      await callback();
    },
  };
}

test("the menu selects the explicit target without submitting or creating a run", async () => {
  const p = page();
  p.start();
  await p.check();
  assert.equal(p.controls.selected, "2");
  assert.deepEqual(JSON.parse(p.alerts[0]), { requested: "2", selected: "2: 192.168.0.12", ready: true, enqueued: false });
  assert.equal(p.controls.clicks, 0);
  assert.deepEqual(p.posted, []);
  assert.equal(p.storage.size, 0);
});

test("preparation waits for real options and an enabled button", async () => {
  const p = page();
  p.controls.options = [{ value: "", text: "No available options", disabled: true }];
  p.controls.buttonDisabled = true;
  p.controls.onTimer = () => {
    if (p.controls.ticks === 2) p.controls.options = options;
    if (p.controls.ticks === 4) p.controls.buttonDisabled = false;
  };
  p.start();
  await p.check();
  assert.match(p.alerts[0], /"ready": true/);
  assert.equal(p.controls.clicks, 0);
  assert.equal(p.controls.ticks, 4);
});

for (const mode of ["unknown target", "ambiguous target", "selection ignored", "disabled", "aria-disabled", "missing button"]) {
  test(`preparation reports ${mode} without registering a job`, async () => {
    const p = page();
    if (mode === "unknown target") p.controls.target = "9";
    if (mode === "ambiguous target") p.controls.options.push({ ...options[1], value: "3" });
    if (mode === "selection ignored") p.controls.ignoreSelection = true;
    if (mode === "disabled") p.controls.buttonDisabled = true;
    if (mode === "aria-disabled") p.controls.ariaDisabled = true;
    if (mode === "missing button") p.controls.buttonPresent = false;
    p.start();
    await p.check();
    assert.match(p.alerts[0], /^prepare:/);
    assert.equal(p.controls.clicks, 0);
    assert.equal(p.storage.size, 0);
    assert.deepEqual(p.posted, []);
  });
}

for (const phase of ["enqueue", "enqueuing", "waiting", "unreported"]) {
  test(`the menu refuses a tab with a ${phase} run`, async () => {
    const p = page();
    p.start();
    p.storage.set(storageKey, JSON.stringify({ runId: "active", phase }));
    await p.check();
    assert.match(p.alerts[0], /Open a separate portal tab/);
    assert.equal(p.controls.prompts, 0);
    assert.equal(p.controls.clicks, 0);
    assert.deepEqual(p.phases, []);
  });
}

test("a second check cannot overlap the first", async () => {
  const p = page();
  p.start();
  const first = p.check();
  await p.check();
  await first;
  assert.equal(p.controls.prompts, 1);
  assert.match(p.alerts[0], /already running/);
  assert.equal(p.controls.clicks, 0);
});

test("normal runs prepare first and submit exactly once", async () => {
  const p = page(undefined, true);
  p.start();
  assert.equal(p.phases[0], "enqueue");
  assert.equal(p.controls.clicks, 0);
  await setImmediate();
  assert.deepEqual(p.phases, ["enqueue", "enqueuing", "waiting", "done"]);
  assert.equal(p.controls.clicks, 1);
  assert.equal(p.controls.selected, "2");
  assert.equal(p.posted.length, 1);
  assert.equal(JSON.parse(p.posted[0]).score, 42);
});

test("submission rechecks the selected target and button", async () => {
  for (const change of ["target", "label", "button"]) {
    const p = page(undefined, true);
    p.controls.beforeSubmit = () => {
      if (change === "target") p.controls.value = "1";
      else if (change === "label") p.controls.options[1].text = "9: 192.168.0.99";
      else p.controls.buttonDisabled = true;
    };
    p.start();
    await setImmediate();
    assert.equal(p.controls.clicks, 0);
    assert.match(JSON.parse(p.posted[0]).error, /selected server changed|button is missing or disabled/);
  }
});

test("a reload during preparation can prepare again", async () => {
  const state = { runId: "test", receiver: "http://localhost:7878", target: "2", phase: "enqueue", startedAt: 0 };
  const p = page(state);
  p.start();
  await setImmediate();
  assert.equal(p.controls.clicks, 1);
  assert.equal(p.posted.length, 1);
});

test("a reload after submission begins never submits again", async () => {
  const state = { runId: "test", receiver: "http://localhost:7878", target: "2", phase: "enqueuing", startedAt: 0 };
  const p = page(state);
  p.start();
  await setImmediate();
  assert.equal(p.controls.clicks, 0);
  assert.match(JSON.parse(p.posted[0]).error, /check the portal/);
});

test("an empty target leaves the page and run state unchanged", async () => {
  const p = page();
  p.controls.target = " ";
  p.start();
  await p.check();
  assert.equal(p.controls.selected, "1");
  assert.equal(p.controls.clicks, 0);
  assert.equal(p.storage.size, 0);
  assert.deepEqual(p.alerts, []);
});

test("checking in a completed tab preserves its completed run", async () => {
  const p = page();
  p.start();
  const state = JSON.stringify({ runId: "finished", phase: "done" });
  p.storage.set(storageKey, state);
  await p.check();
  assert.match(p.alerts[0], /"ready": true/);
  assert.equal(p.storage.get(storageKey), state);
  assert.deepEqual(p.phases, []);
});

test("failed preparation never enters the submission phase", async () => {
  const p = page(undefined, true);
  p.controls.ignoreSelection = true;
  p.start();
  await setImmediate();
  assert.deepEqual(p.phases, ["enqueue", "done"]);
  assert.equal(p.controls.clicks, 0);
  assert.match(JSON.parse(p.posted[0]).error, /selected server/);
});

test("a reload on the new job page resumes result collection without preparation", async () => {
  const p = page({ runId: "test", receiver: "http://localhost:7878", target: "2", phase: "enqueuing", startedAt: 0 });
  p.location.pathname = "/contestant/benchmark_jobs/1";
  p.start();
  await setImmediate();
  assert.deepEqual(p.phases, ["waiting", "done"]);
  assert.equal(p.controls.clicks, 0);
  assert.equal(p.controls.selected, "1");
  assert.equal(JSON.parse(p.posted[0]).score, 42);
});
