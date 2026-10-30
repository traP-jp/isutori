import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { setImmediate } from "node:timers/promises";
import { runInNewContext } from "node:vm";

const source = readFileSync(new URL("../../internal/userscript/isutori.user.js", import.meta.url), "utf8");
const storageKey = "isutori-autobench-v2";
const options = [
  { value: "1", text: "1: 192.168.0.11", disabled: false },
  { value: "2", text: "2: 192.168.0.12", disabled: false },
];

function page(initialState?: object, automatic = false) {
  const badges: { textContent: string }[] = [];
  const menus = new Map<string, () => void | Promise<void>>();
  const storage = new Map<string, string>();
  if (initialState) storage.set(storageKey, JSON.stringify(initialState));
  const alerts: string[] = [];
  const posted: string[] = [];
  const beats: string[] = [];
  const phases: string[] = [];
  const location = {
    pathname: "/contestant",
    search: "",
    hash: automatic ? "#autobench_v2=test&receiver=http://localhost:7878&target=2" : "",
  };
  const controls = {
    verification: "matched",
    running: false,
    jobId: "1",
    status: "FINISHED",
    resultTags: ["Finished", "Passed"],
    scoreText: "42",
    handshakes: 0,
    target: "2",
    options: options.map((option) => ({ ...option })),
    value: "1",
    selected: "1",
    selectDisabled: false,
    buttonDisabled: false,
    ariaDisabled: false,
    buttonPresent: true,
    ignoreSelection: false,
    failHeartbeats: false,
    clicks: 0,
    prompts: 0,
    ticks: 0,
    onTimer: () => {},
    beforeSubmit: () => {},
  };
  const select = {
    get options() {
      return controls.options;
    },
    get value() {
      return controls.value;
    },
    set value(value: string) {
      controls.value = value;
    },
    get selectedOptions() {
      return controls.options.filter((option) => option.value === controls.value);
    },
    matches: () => controls.selectDisabled,
    dispatchEvent() {
      if (controls.ignoreSelection) controls.value = "1";
      controls.selected = controls.value;
    },
  };
  const button = {
    textContent: "Enqueue",
    matches: () => controls.buttonDisabled,
    getAttribute: () => (controls.ariaDisabled ? "true" : null),
    click() {
      controls.clicks++;
      location.pathname = "/contestant/benchmark_jobs/1";
    },
  };
  const cards = ["Summary", "Conclusion", "Result"].map((title) => ({
    get innerText() {
      return title === "Summary" ? `ID: ${controls.jobId}` : `Score: ${controls.scoreText}`;
    },
    querySelector(selector: string) {
      if (selector === ".card-header-title") return { textContent: title };
      if (selector === ".tag") return { textContent: controls.running ? "RUNNING" : controls.status };
      return { innerText: "official result" };
    },
    querySelectorAll() {
      return controls.resultTags.map((textContent) => ({ textContent }));
    },
  }));
  const document = {
    querySelector(selector: string) {
      return selector === "select" ? select : null;
    },
    querySelectorAll(selector: string) {
      if (selector === "button") return controls.buttonPresent ? [button] : [];
      if (selector === ".card") return cards;
      return [];
    },
    createElement() {
      return { style: {}, textContent: "", setAttribute() {} };
    },
    body: {
      append(element: { textContent: string }) {
        badges.push(element);
      },
    },
  };
  let now = 0;
  const sandbox = {
    document,
    location,
    URLSearchParams,
    Event,
    Date: { now: () => now },
    history: {
      replaceState() {
        location.hash = "";
      },
    },
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
    prompt() {
      controls.prompts++;
      return controls.target;
    },
    alert: (value: string) => alerts.push(value),
    GM_registerMenuCommand: (name: string, callback: () => void | Promise<void>) => menus.set(name, callback),
    GM_xmlhttpRequest(request: {
      url: string;
      data: string;
      onload: (response: { status: number; responseText: string }) => void;
      onerror?: () => void;
    }) {
      if (request.url.endsWith("/api/userscript")) {
        controls.handshakes++;
        request.onload({
          status: 200,
          responseText: JSON.stringify({
            expected: "sha256:expected",
            actual: "sha256:actual",
            status: controls.verification,
          }),
        });
        return;
      }
      if (request.url.endsWith("/api/heartbeat")) {
        beats.push(request.data);
        if (controls.failHeartbeats) request.onerror?.();
        else request.onload({ status: 204, responseText: "" });
        return;
      }
      posted.push(request.data);
      request.onload({ status: 204, responseText: "" });
    },
  };
  return {
    controls,
    alerts,
    posted,
    beats,
    phases,
    storage,
    location,
    badges,
    start() {
      runInNewContext(source, sandbox);
    },
    read() {
      const callback = menus.get("isutori: read this page");
      assert.ok(callback);
      callback();
      return alerts.at(-1);
    },
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
  assert.deepEqual(JSON.parse(p.alerts[0]), {
    requested: "2",
    selected: "2: 192.168.0.12",
    ready: true,
    enqueued: false,
  });
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

for (const mode of [
  "unknown target",
  "ambiguous target",
  "selection ignored",
  "disabled",
  "aria-disabled",
  "missing button",
]) {
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

test("an unverified script never prepares or registers", async () => {
  for (const status of ["mismatched", "unknown"]) {
    const p = page(undefined, true);
    p.controls.verification = status;
    p.start();
    await setImmediate();
    assert.equal(p.controls.handshakes, 1);
    assert.equal(p.controls.selected, "1");
    assert.equal(p.controls.clicks, 0);
    assert.equal(p.posted.length, 0);
    assert.match(JSON.parse(p.storage.get(storageKey) ?? "null").note, /BLOCKED/);
  }
});

test("the legacy launch URL cannot start a new automatic run", async () => {
  const p = page(undefined, true);
  p.location.hash = "#autobench=old&receiver=http://localhost:7878&target=2";
  p.start();
  await setImmediate();
  assert.equal(p.controls.handshakes, 0);
  assert.equal(p.controls.clicks, 0);
  assert.equal(p.storage.size, 0);
});

test("a blocked run can verify on reload without duplicate registration", async () => {
  const p = page(undefined, true);
  p.controls.verification = "mismatched";
  p.start();
  await setImmediate();
  assert.equal(p.controls.clicks, 0);
  p.controls.verification = "matched";
  p.start();
  await setImmediate();
  assert.equal(p.controls.clicks, 1);
  assert.equal(p.posted.length, 1);
  assert.equal(p.controls.handshakes, 2);
  p.start();
  await setImmediate();
  assert.equal(p.controls.clicks, 1);
  assert.equal(p.controls.handshakes, 2);
});

test("verification recovery clears the blocked badge while the job is still running", async () => {
  const p = page({
    runId: "test",
    receiver: "http://localhost:7878",
    target: "2",
    phase: "waiting",
    job: { jobId: "1" },
    startedAt: 0,
    note: "BLOCKED: userscript verification: mismatched",
  });
  p.location.pathname = "/contestant/benchmark_jobs/1";
  p.controls.running = true;
  let checked = false;
  p.controls.onTimer = () => {
    assert.doesNotMatch(p.badges[0].textContent, /BLOCKED/);
    assert.equal(JSON.parse(p.storage.get(storageKey) ?? "null").note, undefined);
    checked = true;
    p.controls.running = false;
  };
  p.start();
  await setImmediate();
  assert.ok(checked);
  assert.equal(p.controls.clicks, 0);
  assert.equal(p.posted.length, 1);
});

for (const state of ["running", "pending", "old job", "result pending", "missing verdict", "missing score"]) {
  test(`result reader waits for ${state}`, () => {
    const p = page();
    p.location.pathname = "/contestant/benchmark_jobs/1";
    if (state === "running") p.controls.running = true;
    if (state === "pending") p.controls.status = "PENDING";
    if (state === "old job") p.controls.jobId = "9";
    if (state === "result pending") p.controls.resultTags = ["In progress"];
    if (state === "missing verdict") p.controls.resultTags = ["Finished"];
    if (state === "missing score") p.controls.scoreText = "";
    p.start();
    assert.equal(p.read(), "null");
    assert.equal(p.posted.length, 0);
  });
}

for (const state of [
  "wrong page",
  "unknown status",
  "conflicting verdicts",
  "partial number",
  "fraction",
  "unsafe integer",
]) {
  test(`result reader rejects ${state}`, () => {
    const p = page();
    p.location.pathname = state === "wrong page" ? "/contestant" : "/contestant/benchmark_jobs/1";
    if (state === "unknown status") p.controls.status = "UNKNOWN";
    if (state === "conflicting verdicts") p.controls.resultTags = ["Finished", "Passed", "Failed"];
    if (state === "partial number") p.controls.scoreText = "42pending";
    if (state === "fraction") p.controls.scoreText = "42.5";
    if (state === "unsafe integer") p.controls.scoreText = "9007199254740993";
    p.start();
    assert.match(p.read() ?? "", /^readResult threw:/);
  });
}

for (const outcome of ["passed", "failed", "errored"]) {
  test(`result reader returns a final ${outcome} benchmark`, () => {
    const p = page();
    p.location.pathname = "/contestant/benchmark_jobs/1";
    if (outcome === "failed") {
      p.controls.resultTags = ["Finished", "Failed"];
      p.controls.scoreText = "0";
    }
    if (outcome === "errored") p.controls.status = "ERRORED";
    p.start();
    const result = JSON.parse(p.read() ?? "null");
    assert.equal(result.passed, outcome === "passed");
    assert.equal(result.score, outcome === "passed" ? 42 : 0);
  });
}

test("automatic result collection reports navigation to another job as an acquisition error", async () => {
  const p = page({
    runId: "test",
    receiver: "http://localhost:7878",
    target: "2",
    phase: "waiting",
    job: { jobId: "2" },
    startedAt: 0,
  });
  p.location.pathname = "/contestant/benchmark_jobs/1";
  p.start();
  await setImmediate();
  const result = JSON.parse(p.posted[0]);
  assert.match(result.error, /left the job page/);
  assert.equal(result.score, undefined);
});

test("a job that never publishes a final result times out without publishing its score", async () => {
  const p = page({
    runId: "test",
    receiver: "http://localhost:7878",
    target: "2",
    phase: "waiting",
    job: { jobId: "1" },
    startedAt: -20 * 60 * 1000,
  });
  p.location.pathname = "/contestant/benchmark_jobs/1";
  p.controls.running = true;
  p.start();
  await setImmediate();
  const result = JSON.parse(p.posted[0]);
  assert.match(result.error, /timed out/);
  assert.equal(result.score, undefined);
});

test("a result already on the page is reported even after the old timeout", async () => {
  const p = page({
    runId: "test",
    receiver: "http://localhost:7878",
    target: "2",
    phase: "waiting",
    job: { jobId: "1" },
    startedAt: -21 * 60 * 1000,
  });
  p.location.pathname = "/contestant/benchmark_jobs/1";
  p.start();
  await setImmediate();
  const result = JSON.parse(p.posted[0]);
  assert.equal(result.score, 42);
  assert.equal(result.error, undefined);
});

test("a deadline from the CLI extends the wait past the old timeout", async () => {
  const p = page({
    runId: "test",
    receiver: "http://localhost:7878",
    target: "2",
    phase: "waiting",
    job: { jobId: "1" },
    startedAt: -21 * 60 * 1000,
    deadline: 60 * 1000,
  });
  p.location.pathname = "/contestant/benchmark_jobs/1";
  p.controls.running = true;
  p.controls.onTimer = () => {
    p.controls.running = false;
  };
  p.start();
  await setImmediate();
  const result = JSON.parse(p.posted[0]);
  assert.equal(result.score, 42);
});

test("a past deadline with no result reports a timeout", async () => {
  const p = page({
    runId: "test",
    receiver: "http://localhost:7878",
    target: "2",
    phase: "waiting",
    job: { jobId: "1" },
    startedAt: 0,
    deadline: 1000,
  });
  p.location.pathname = "/contestant/benchmark_jobs/1";
  p.controls.running = true;
  p.start();
  await setImmediate();
  const result = JSON.parse(p.posted[0]);
  assert.match(result.error, /timed out/);
  assert.equal(result.score, undefined);
});

test("heartbeats are sent while waiting for the result", async () => {
  const p = page({
    runId: "test",
    receiver: "http://localhost:7878",
    target: "2",
    phase: "waiting",
    job: { jobId: "1" },
    startedAt: 0,
  });
  p.location.pathname = "/contestant/benchmark_jobs/1";
  p.controls.running = true;
  p.controls.onTimer = () => {
    if (p.controls.ticks === 2) p.controls.running = false;
  };
  p.start();
  await setImmediate();
  assert.equal(JSON.parse(p.posted[0]).score, 42);
  assert.equal(p.beats.length, 2);
  assert.ok(p.beats.every((beat) => JSON.parse(beat).run_id === "test"));
});

test("a failed heartbeat does not stop result collection", async () => {
  const p = page({
    runId: "test",
    receiver: "http://localhost:7878",
    target: "2",
    phase: "waiting",
    job: { jobId: "1" },
    startedAt: 0,
  });
  p.location.pathname = "/contestant/benchmark_jobs/1";
  p.controls.running = true;
  p.controls.failHeartbeats = true;
  p.controls.onTimer = () => {
    p.controls.running = false;
  };
  p.start();
  await setImmediate();
  assert.equal(JSON.parse(p.posted[0]).score, 42);
  assert.ok(p.beats.length >= 1);
});

test("the launch URL deadline is kept in the run state", async () => {
  const p = page(undefined, true);
  p.location.hash = "#autobench_v2=test&receiver=http://localhost:7878&target=2&deadline=60000";
  p.start();
  await setImmediate();
  assert.equal(JSON.parse(p.storage.get(storageKey) ?? "null").deadline, 60000);
});

test("an invalid deadline in the launch URL is ignored", async () => {
  const p = page(undefined, true);
  p.location.hash = "#autobench_v2=test&receiver=http://localhost:7878&target=2&deadline=abc";
  p.start();
  await setImmediate();
  assert.equal(JSON.parse(p.storage.get(storageKey) ?? "null").deadline, undefined);
  assert.equal(JSON.parse(p.posted[0]).score, 42);
});
