// ==UserScript==
// @name         isutori autobench
// @namespace    https://github.com/traP-jp/isutori
// @version      9
// @description  Records portal DOM changes and runs benchmarks after checking the script identity.
// @match        http://127.0.0.1:7879/*
// @grant        GM_xmlhttpRequest
// @grant        GM_registerMenuCommand
// @grant        GM_getTab
// @grant        GM_saveTab
// @grant        GM_getValue
// @grant        GM_setValue
// @grant        GM_addValueChangeListener
// @grant        window.close
// @connect      127.0.0.1
// @connect      localhost
// @run-at       document-idle
// ==/UserScript==

// @ts-check

(function isutoriScript() {
  let inspectionBusy = () => false;
  let checkTabCleanup = () => {};
  /** @typedef {{ expected: string, actual: string, status: string, error?: string }} Verification */
  function scriptSource() { return Function.prototype.toString.call(isutoriScript); }
  /** @param {Verification | undefined} value */
  function verificationText(value) {
    return `${value?.status}; expected=${value?.expected.slice(0, 19) || "unavailable"} actual=${value?.actual.slice(0, 19) || "unavailable"}${value?.error ? `; ${value.error}` : ""}`;
  }

(function () {
  "use strict";
  if (typeof window !== "undefined" && window.top !== window.self) return;

  const key = "isutori-inspect";
  /** @typedef {{ endpoint: string, token: string }} Connection */
  /** @typedef {{ page: string, sequence: number, at: string, url: string, reason: string, target: string, visibility: string, script_id: string, html?: string }} InspectionEvent */
  /** @type {Connection | undefined} */
  let connection;
  /** @type {MutationObserver | undefined} */
  let observer;
  /** @type {ReturnType<typeof setInterval> | undefined} */
  let timer;
  /** @type {HTMLDivElement | undefined} */
  let status;
  /** @type {InspectionEvent[]} */
  let pending = [];
  let pendingBytes = 0;
  let sending = false;
  let starting = false;
  let lastHTML = "";
  let page = "";
  let sequence = 0;
  /** @type {Verification | undefined} */
  let identity;
  let active = false;
  let stoppedByUser = false;
  inspectionBusy = () => active || starting || sending || pending.length > 0;
  const eventTypes = ["click", "input", "change", "visibilitychange"];

  /** @param {string} message */
  function show(message) {
    if (!status) {
      status = document.createElement("div");
      status.setAttribute("data-isutori-inspect", "");
      status.style.cssText = "position:fixed;right:.5rem;bottom:.5rem;z-index:99999;background:#222;color:#fff;padding:.5rem;font:12px monospace;max-width:calc(100vw - 1rem);overflow-wrap:anywhere";
      document.body.append(status);
    }
    status.textContent = `isutori recording: ${message}`;
  }

  /** @param {string} message */
  function stop(message) {
    active = false;
    observer?.disconnect();
    clearInterval(timer);
    for (const type of eventTypes) document.removeEventListener(type, onEvent, true);
    window.removeEventListener("pagehide", onEvent, true);
    sessionStorage.removeItem(key);
    show(message);
  }

  /** @param {string} text @returns {Connection} */
  function parseConnection(text) {
    const url = new URL(text);
    if (url.protocol !== "http:" || !["127.0.0.1", "localhost"].includes(url.hostname) || url.pathname !== "/" || url.username || url.password || url.search || !/^[A-Za-z0-9_-]{20,100}$/.test(url.hash.slice(1))) {
      throw new Error("Use the loopback connection printed by isutori inspect.");
    }
    return { endpoint: `${url.origin}/api/inspect`, token: url.hash.slice(1) };
  }

  /** @param {Connection} config @param {InspectionEvent | {page: string, source: string}} event @returns {Promise<string>} */
  function request(config, event, handshake = false) {
    return new Promise((resolve, reject) => {
      GM_xmlhttpRequest({
        method: "POST", url: config.endpoint + (handshake ? "/start" : ""),
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${config.token}` },
        data: JSON.stringify(event),
        timeout: 10000,
        onload: (response) => response.status >= 200 && response.status < 300 ? resolve(response.responseText) : reject(new Error(`${response.status}: ${response.responseText}`)),
        onerror: () => reject(new Error("receiver disconnected")),
        ontimeout: () => reject(new Error("receiver timed out")),
        onabort: () => reject(new Error("request aborted")),
      });
    });
  }

  function snapshot() {
    const copy = document.body.cloneNode(true);
    if (!(copy instanceof HTMLElement)) throw new Error("cannot copy the document body");
    const originals = document.body.querySelectorAll("input,select,textarea");
    const copies = copy.querySelectorAll("input,select,textarea");
    originals.forEach((element, index) => {
      const clone = copies[index];
      if (element instanceof HTMLInputElement) {
        if (element.type === "password" || element.type === "hidden" || element.type === "file") {
          clone.removeAttribute("value");
        } else {
          clone.setAttribute("value", element.value);
        }
        clone.toggleAttribute("checked", element.checked);
        clone.toggleAttribute("data-indeterminate", element.indeterminate);
      } else if (element instanceof HTMLTextAreaElement) {
        clone.textContent = element.value;
      } else if (element instanceof HTMLSelectElement && clone instanceof HTMLSelectElement) {
        [...element.options].forEach((option, i) => clone.options[i].toggleAttribute("selected", option.selected));
      }
    });
    copy.querySelectorAll("script,style,meta,[data-isutori-inspect],[data-isutori-badge]").forEach((element) => element.remove());
    return copy.outerHTML;
  }

  async function flush() {
    if (sending || !connection) return;
    sending = true;
    try {
      while (pending.length) {
        const event = pending[0];
        identity = JSON.parse(await request(connection, event));
        pending.shift();
        pendingBytes -= new TextEncoder().encode(JSON.stringify(event)).length;
        if (active) show(`recording; ${verificationText(identity)}; saved ${event.sequence}; pending ${pending.length}`);
        else if (stoppedByUser) show(`stopped; ${pending.length} event(s) pending`);
      }
    } catch (e) {
      stop(`STOPPED: ${e.message}; ${pending.length} unsaved event(s). Keep this page to retry, or reload to discard them.`);
    } finally {
      sending = false;
      checkTabCleanup();
    }
  }

  /** @param {string} reason @param {string} [target] */
  function capture(reason, target = "") {
    if (!active) return;
    try {
      const html = snapshot();
      /** @type {InspectionEvent} */
      const event = { page, sequence: ++sequence, at: new Date().toISOString(), url: location.origin + location.pathname, reason, target, visibility: document.visibilityState, script_id: identity?.actual ?? "" };
      if (html !== lastHTML) event.html = html;
      const bytes = new TextEncoder().encode(JSON.stringify(event)).length;
      if (bytes > 2 * 1024 * 1024 || pending.length >= 100 || pendingBytes + bytes > 8 * 1024 * 1024) {
        stop("STOPPED: recording capacity reached; a state was not captured. Start a new recording.");
        return;
      }
      lastHTML = html;
      pending.push(event);
      pendingBytes += bytes;
      void flush();
    } catch (e) {
      stop(`STOPPED: ${e.message}`);
    }
  }

  /** @param {Event} event */
  function onEvent(event) {
    const element = event.target instanceof Element ? event.target : null;
    if (element?.closest("[data-isutori-inspect],[data-isutori-badge]")) return;
    const path = [];
    for (let node = element; node && node !== document.body; node = node.parentElement) {
      const parent = node.parentElement;
      if (!parent) break;
      path.unshift(`${node.localName}:nth-child(${[...parent.children].indexOf(node) + 1})`);
    }
    const target = element ? JSON.stringify({ path: ["body", ...path].join(" > "), tag: element.tagName, id: element.id, name: element.getAttribute("name"), role: element.getAttribute("role"), type: element.getAttribute("type") }) : "";
    if (event.type === "pagehide" && active && connection) sessionStorage.setItem(key, JSON.stringify({ ...connection, resume: true }));
    capture(event.type, target);
    if (event.type === "click" || event.type === "input" || event.type === "change") {
      setTimeout(() => capture(`${event.type}:after`, target), 0);
    }
  }

  /** @param {Connection} config */
  async function start(config) {
    const newPage = [...crypto.getRandomValues(new Uint8Array(16))].map((byte) => byte.toString(16).padStart(2, "0")).join("");
    const info = JSON.parse(await request(config, {page: newPage, source: scriptSource()}, true));
    if (info.origin !== location.origin) throw new Error(`Expected portal origin ${info.origin}, got ${location.origin}`);
    connection = config;
    if (pending.length) {
      await flush();
      if (pending.length) throw new Error("Previous events could not be saved; receiver must be restored before restarting.");
    }
    page = newPage;
    identity = info.userscript;
    sequence = 0;
    lastHTML = "";
    sessionStorage.setItem(key, JSON.stringify({ ...config, resume: false }));
    active = true;
    stoppedByUser = false;
    show(`recording; ${verificationText(identity)}`);
    capture("start");
    if (!active) return;
    observer = new MutationObserver((records) => {
      if (records.some((record) => !(record.target instanceof Element ? record.target : record.target.parentElement)?.closest("[data-isutori-inspect],[data-isutori-badge]"))) capture("mutation");
    });
    observer.observe(document.body, { subtree: true, childList: true, attributes: true, characterData: true });
    for (const type of eventTypes) document.addEventListener(type, onEvent, true);
    window.addEventListener("pagehide", onEvent, true);
    timer = setInterval(() => capture("tick"), 1000);
  }

  GM_registerMenuCommand("isutori: start recording", async () => {
    if (active || starting || sending) return alert("Recording is active or a request is still pending.");
    const value = prompt("Paste the connection printed by isutori inspect:");
    if (!value) return;
    starting = true;
    try {
      const config = parseConnection(value.trim());
      if (pending.length && connection && (config.endpoint !== connection.endpoint || config.token !== connection.token)) throw new Error("Unsaved events belong to the previous receiver; reload to discard them explicitly.");
      await start(config);
    } catch (e) {
      show(`STOPPED: ${e.message}`);
    } finally {
      starting = false;
      checkTabCleanup();
    }
  });
  GM_registerMenuCommand("isutori: stop recording", () => {
    if (starting) return alert("Recording is starting; try stopping after it connects.");
    if (!active) return;
    capture("stop");
    stoppedByUser = true;
    stop(`stopped; ${pending.length} event(s) pending`);
  });

  let saved;
  try { saved = sessionStorage.getItem(key); } catch { return; }
  if (saved) {
    starting = true;
    Promise.resolve().then(() => {
      const config = JSON.parse(saved);
      const navigation = performance.getEntriesByType("navigation")[0];
      if (!config.resume && (!(navigation instanceof PerformanceNavigationTiming) || navigation.type !== "reload")) {
        sessionStorage.removeItem(key);
        return;
      }
      return start(parseConnection(`${config.endpoint.replace(/\/api\/inspect$/, "/")}#${config.token}`));
    }).catch((e) => stop(`STOPPED: ${e.message}`)).finally(() => { starting = false; checkTabCleanup(); });
  }
})();

(function () {
  "use strict";
  if (typeof window !== "undefined" && window.top !== window.self) return;

  // ===== PORTAL ADAPTER: rewrite this section for the portal of the day =====
  // The example below targets isucon14-portal (and `isutori mock-portal`).
  // Try readResult on a finished job page with the "read this page" menu
  // command of the userscript manager; it does not enqueue anything.
  // "check enqueue" selects a target and checks readiness without submitting.

  /**
   * Jobs are kept in sessionStorage, so they must survive JSON.
   * @typedef {{ jobId: string }} Job
   */

  /**
   * Preparation may select a server, but must not enqueue or change run state.
   * submit must recheck the target and button before registering exactly one job.
   * Keep this object in memory; only Job is saved across page reloads.
   * @typedef {{ target: string, submit: () => Promise<Job> }} Prepared
   */

  const adapter = {
    /**
     * Prepares target selection without registering a job. Loading placeholders
     * are not real options; the target may determine the final measurement.
     * @param {string} target the TARGET given to `isutori autobench`
     * @returns {Promise<Prepared>}
     */
    async prepare(target) {
      const select = await waitFor("the server options", () => {
        const select = document.querySelector("select");
        return select && !select.matches(":disabled") && [...select.options].some((o) => o.value && !o.disabled) ? select : null;
      });
      const options = [...select.options].filter((o) => o.value && !o.disabled);
      // Options read like "1: 192.168.0.11 (203.0.113.11)"; target may be any of those parts.
      /** @param {string} text */
      const matchesTarget = (text) => text.split(/[\s:()]+/).includes(target);
      const matches = options.filter((o) => matchesTarget(o.text));
      if (matches.length !== 1) {
        throw new Error(`target ${JSON.stringify(target)} matches ${matches.length} of: ${options.map((o) => o.text).join(", ")}; selected server: ${JSON.stringify(select.selectedOptions[0]?.text ?? null)}`);
      }
      const option = matches[0];
      select.value = option.value;
      select.dispatchEvent(new Event("change", { bubbles: true }));
      const enabledButton = () => {
        const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Enqueue");
        return button && !button.matches(":disabled") && button.getAttribute("aria-disabled") !== "true" ? button : null;
      };
      try {
        await waitFor(`selected server ${JSON.stringify(option.text)}`, () => document.querySelector("select")?.value === option.value);
        await waitFor("the enabled Enqueue button", enabledButton);
      } catch (e) {
        throw new Error(`${e.message}; selected server: ${JSON.stringify(document.querySelector("select")?.selectedOptions[0]?.text ?? null)}`);
      }

      const checkedButton = () => {
        const current = document.querySelector("select");
        if (!current || current.matches(":disabled") || current.value !== option.value || !matchesTarget(current.selectedOptions[0]?.text ?? "")) {
          throw new Error(`selected server changed: expected ${JSON.stringify(option.text)}, got ${JSON.stringify(current?.selectedOptions[0]?.text ?? null)}`);
        }
        const button = enabledButton();
        if (!button) throw new Error("Enqueue button is missing or disabled");
        return button;
      };
      checkedButton();
      return {
        target: option.text,
        async submit() {
          checkedButton().click();
          return waitFor("the job page", () => {
            const job = adapter.currentJob();
            if (job) return job;
            const error = document.querySelector(".message.is-danger, .notification.is-danger");
            if (error) throw new Error(`portal refused to enqueue: ${error.textContent?.trim()}`);
            return null;
          });
        },
      };
    },

    /**
     * Used to resume when enqueuing reloads the page.
     * @returns {Job | null} the job shown on the current page.
     */
    currentJob() {
      const m = location.pathname.match(/^\/contestant\/benchmark_jobs\/(\d+)$/);
      return m ? { jobId: m[1] } : null;
    },

    /**
     * Wait for this job's terminal state and final-result fields from the same update.
     * A visible or steady score is not final; failed benchmarks are valid results.
     * Throw on navigation away or unsupported states; never substitute another job.
     * @param {Job | null} job null reads the job shown on the current page.
     * @returns {BenchResult | null} null while running or awaiting final-result fields.
     */
    readResult(job) {
      const current = adapter.currentJob();
      if (!current || (job && current.jobId !== job.jobId)) {
        throw new Error(`left the job page: ${location.pathname}`);
      }
      /** @param {string} title */
      const card = (title) =>
        [.../** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll(".card"))].find(
          (c) => c.querySelector(".card-header-title")?.textContent?.trim() === title,
        );
      const summary = card("Summary");
      if (!summary) return null;
      const displayedJob = summary.innerText.match(/^ID:\s*(\d+)\s*$/m)?.[1];
      if (displayedJob !== current.jobId) return null;
      const status = summary.querySelector(".tag")?.textContent?.trim();
      if (!status || status === "PENDING" || status === "RUNNING") return null;
      const conclusion =
        /** @type {HTMLElement | null | undefined} */ (card("Conclusion")?.querySelector(".card-content"))?.innerText.trim() ?? "";
      if (status === "ERRORED") {
        return { passed: false, score: 0, message: `ERRORED\n${conclusion}`.trim() };
      }
      if (status !== "FINISHED") throw new Error(`unsupported job status: ${status}`);
      const result = card("Result");
      const tags = [...(result?.querySelectorAll(".tag") ?? [])].map((t) => t.textContent?.trim());
      if (!tags.includes("Finished")) return null;
      const passed = tags.includes("Passed");
      const failed = tags.includes("Failed");
      if (passed && failed) throw new Error("conflicting result verdicts");
      if (!passed && !failed) return null;
      const scoreText = result?.innerText.match(/^Score:[^\S\r\n]*(.*)$/m)?.[1].trim();
      if (scoreText === undefined || scoreText === "") return null;
      if (!/^-?\d+$/.test(scoreText) || !Number.isSafeInteger(Number(scoreText))) {
        throw new Error(`invalid final score: ${scoreText}`);
      }
      return { passed, score: Number(scoreText), message: conclusion };
    },
  };
  // ===== END OF PORTAL ADAPTER =====

  /** @typedef {{ passed: boolean, score: number, message: string }} BenchResult */

  /** @typedef {(BenchResult | { error: string }) & {userscript?: Verification}} Report */

  /**
   * @typedef {object} Run
   * @property {string} runId
   * @property {string} receiver
   * @property {string} target
   * @property {number} startedAt epoch milliseconds
   * @property {string} [note]
   * @property {boolean} [autoOpened]
   * @property {{order: number, received?: boolean, keep?: boolean, job?: Job}} [tab]
   */

  /**
   * @typedef {Run & ({ phase: "enqueue" | "enqueuing" | "done" | "unreported" } | { phase: "waiting", job: Job })} State
   */

  const pollMs = 2000;
  const enqueueTimeoutMs = 60 * 1000;
  const resultTimeoutMs = 20 * 60 * 1000;
  const storageKey = "isutori-autobench-v2";
  /** @type {Verification | undefined} */
  let verified;
  const cleanupEnabledKey = "isutori-tab-cleanup";
  let launchedFromHash = false;
  /** @type {{isutoriRun?: string} | undefined} */
  let browserTab;
  /** @typedef {{runId: string, order: number, receivedOrder: number}} TabGroup */
  /** @param {State} state */
  function tabGroupKey(state) {
    return `isutori-tabs:${location.origin}:${new URL(state.receiver).origin}`;
  }

  /** @param {State} state @returns {State} */
  function registerTab(state) {
    if (!state.autoOpened || state.tab || !browserTab || (!launchedFromHash && browserTab.isutoriRun !== state.runId) || typeof GM_getValue !== "function") return state;
    try {
      const key = tabGroupKey(state);
      const latest = /** @type {TabGroup | null} */ (GM_getValue(key, null));
      const order = latest?.runId === state.runId ? latest.order : (latest?.order ?? 0) + 1;
      browserTab.isutoriRun = state.runId;
      GM_saveTab(browserTab);
      state = {...state, tab: {order}};
      saveState(state);
      if (latest?.runId !== state.runId) GM_setValue(key, {runId: state.runId, order, receivedOrder: latest?.receivedOrder ?? 0});
    } catch (e) {
      console.warn("isutori: tab cleanup unavailable", e);
    }
    return state;
  }

  checkTabCleanup = () => {
    try {
      const state = loadState();
      if (!state?.autoOpened || browserTab?.isutoriRun !== state.runId || !state.tab?.received || state.tab.keep || state.phase !== "done" || inspectionBusy() ||
          typeof GM_getValue !== "function" || !GM_getValue(cleanupEnabledKey, false)) return;
      const current = adapter.currentJob();
      if (!current || current.jobId !== state.tab.job?.jobId) return;
      const latest = /** @type {TabGroup | null} */ (GM_getValue(tabGroupKey(state), null));
      if (latest && latest.receivedOrder > state.tab.order) window.close();
    } catch (e) {
      console.warn("isutori: could not close old tab", e);
    }
  };

  /** @param {State} state */
  function notifyResultReceived(state) {
    if (!state.tab?.received || typeof GM_getValue !== "function") return;
    try {
      const key = tabGroupKey(state);
      const latest = /** @type {TabGroup | null} */ (GM_getValue(key, null));
      if (latest && latest.order >= state.tab.order && latest.receivedOrder < state.tab.order) {
        GM_setValue(key, {...latest, receivedOrder: state.tab.order});
      }
    } catch (e) {
      console.warn("isutori: tab cleanup notification failed", e);
    }
  }

  /**
   * @template T
   * @param {string} what named in the timeout error
   * @param {() => T | null | undefined} fn
   * @returns {Promise<T>}
   */
  async function waitFor(what, fn, timeoutMs = enqueueTimeoutMs) {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const v = fn();
      if (v) return v;
      if (Date.now() > deadline) throw new Error(`timed out after ${timeoutMs / 1000}s waiting for ${what}`);
      await sleep(200);
    }
  }

  /** @param {number} ms */
  function sleep(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  /** @returns {State | null} */
  function loadState() {
    try {
      return JSON.parse(sessionStorage.getItem(storageKey) ?? "null");
    } catch {
      return null;
    }
  }

  /** @param {State} state */
  function saveState(state) {
    sessionStorage.setItem(storageKey, JSON.stringify(state));
    show(state);
  }

  /** @returns {State | null} */
  function stateFromHash() {
    const params = new URLSearchParams(location.hash.slice(1));
    const runId = params.get("autobench_v2");
    const receiver = params.get("receiver");
    const target = params.get("target");
    if (!runId || !receiver || !target) return null;
    launchedFromHash = true;
    history.replaceState(null, "", location.pathname + location.search);
    const current = loadState();
    if (current?.runId === runId) return current;
    return { runId, receiver, target, phase: "enqueue", startedAt: Date.now(), autoOpened: true };
  }

  /**
   * @param {State} state
   * @param {Report | {source: string}} body
   * @returns {Promise<string>}
   */
  function post(state, body, path = "/api/result") {
    return new Promise((resolve, reject) => {
      GM_xmlhttpRequest({
        method: "POST",
        url: `${state.receiver}${path}`,
        headers: { "Content-Type": "application/json" },
        data: JSON.stringify({ run_id: state.runId, ...body }),
        onload: (res) =>
          res.status >= 200 && res.status < 300 ? resolve(res.responseText) : reject(new Error(`${res.status} ${res.responseText}`)),
        onerror: () => reject(new Error(`cannot reach ${state.receiver}`)),
        ontimeout: () => reject(new Error(`timed out posting to ${state.receiver}`)),
        timeout: 10000,
      });
    });
  }

  /**
   * @param {State} state
   * @param {Report} body
   */
  async function report(state, body) {
    try {
      await post(state, {...body, userscript: verified});
      const saved = loadState();
      const tab = saved?.runId === state.runId ? saved.tab : state.tab;
      state = {...state, phase: "done", tab: tab && {...tab, received: !("error" in body), job: "job" in state ? state.job : undefined}, note: "error" in body ? body.error : `passed=${body.passed} score=${body.score}`};
      saveState(state);
    } catch (e) {
      const saved = loadState();
      const tab = saved?.runId === state.runId ? saved.tab : state.tab;
      saveState({ ...state, tab, phase: "unreported", note: `could not report to isutori: ${e.message}` });
      return;
    }
    notifyResultReceived(state);
    checkTabCleanup();
  }

  /** @param {State} state */
  async function run(state) {
    if (state.phase === "done" || state.phase === "unreported") { show(state); return; }
    if (state.phase === "enqueue") saveState(state); else show(state);
    try {
      verified = JSON.parse(await post(state, {source: scriptSource()}, "/api/userscript"));
      if (verified?.status !== "matched") throw new Error(verificationText(verified));
      if (state.note) {
        state = {...state, note: undefined};
        saveState(state);
      }
    } catch (e) {
      saveState({...state, note: `BLOCKED: userscript verification: ${e.message}; use the manual result form or install the expected script and reload`});
      return;
    }
    state = registerTab(state);
    if (state.phase === "enqueue") {
      try {
        const prepared = await adapter.prepare(state.target);
        saveState({ ...state, phase: "enqueuing" });
        const job = await prepared.submit();
        state = { ...state, phase: "waiting", job };
        saveState(state);
      } catch (e) {
        return report(state, { error: `enqueue: ${e.message}` });
      }
    } else if (state.phase === "enqueuing") {
      // Never resubmit after reload: registration may already have succeeded.
      try {
        const job = await waitFor("the job page", () => adapter.currentJob(), 15 * 1000);
        state = { ...state, phase: "waiting", job };
        saveState(state);
      } catch {
        return report(state, { error: "page was reloaded while enqueuing and no job page followed; check the portal" });
      }
    }
    if (state.phase !== "waiting") {
      show(state);
      return;
    }
    for (;;) {
      if (Date.now() - state.startedAt > resultTimeoutMs) {
        return report(state, { error: "timed out waiting for the result" });
      }
      let result;
      try {
        result = adapter.readResult(state.job);
      } catch (e) {
        return report(state, { error: `readResult: ${e.message}` });
      }
      if (result) return report(state, result);
      await sleep(pollMs);
    }
  }

  /** @type {HTMLDivElement | undefined} */
  let badge;
  /** @param {State} state */
  function show(state) {
    if (!badge) {
      badge = document.createElement("div");
      badge.setAttribute("data-isutori-badge", "");
      badge.style.cssText =
        "position:fixed;left:.5rem;bottom:.5rem;z-index:99999;padding:.25rem .5rem;" +
        "background:#222;color:#fff;font:12px monospace;border-radius:4px;white-space:pre-wrap;max-width:calc(100vw - 1rem);overflow-wrap:anywhere";
      document.body.append(badge);
    }
    badge.textContent = `isutori ${state.runId}: ${state.phase}${state.note ? `\n${state.note}` : ""}`;
  }

  GM_registerMenuCommand("isutori: read this page", () => {
    try {
      alert(JSON.stringify(adapter.readResult(null), null, 2));
    } catch (e) {
      alert(`readResult threw: ${e.message}`);
    }
  });

  let checking = false;
  GM_registerMenuCommand("isutori: check enqueue", async () => {
    if (checking) return alert("An enqueue check is already running.");
    const state = loadState();
    if (state && state.phase !== "done") {
      return alert(`This tab has run ${state.runId} (${state.phase}). Open a separate portal tab for the check.`);
    }
    const target = prompt("Target server (selection will change; no benchmark will be enqueued):")?.trim();
    if (!target) return;
    checking = true;
    try {
      const prepared = await adapter.prepare(target);
      alert(JSON.stringify({ requested: target, selected: prepared.target, ready: true, enqueued: false }, null, 2));
    } catch (e) {
      alert(`prepare: target ${JSON.stringify(target)}: ${e.message}`);
    } finally {
      checking = false;
    }
  });

  GM_registerMenuCommand("isutori: toggle automatic tab cleanup", () => {
    if (typeof GM_getValue !== "function") return alert("Tab cleanup requires Tampermonkey storage permissions; reinstall the userscript.");
    try {
      const enabled = !GM_getValue(cleanupEnabledKey, false);
      GM_setValue(cleanupEnabledKey, enabled);
      alert(`Automatic tab cleanup ${enabled ? "enabled" : "disabled"}.`);
      checkTabCleanup();
    } catch (e) { alert(`Tab cleanup unavailable: ${e.message}`); }
  });
  GM_registerMenuCommand("isutori: toggle keep this tab", () => {
    const state = loadState();
    if (!state?.tab || browserTab?.isutoriRun !== state.runId) return alert("This tab is not managed by automatic tab cleanup.");
    const keep = !state.tab.keep;
    saveState({...state, tab: {...state.tab, keep}});
    alert(keep ? "This tab will be kept." : "This tab can be cleaned up.");
    checkTabCleanup();
  });

  const state = stateFromHash() ?? loadState();
  try {
    if (typeof GM_addValueChangeListener === "function") {
      GM_addValueChangeListener(cleanupEnabledKey, checkTabCleanup);
      if (state) GM_addValueChangeListener(tabGroupKey(state), checkTabCleanup);
    }
  } catch (e) { console.warn("isutori: tab cleanup listener unavailable", e); }
  const start = () => { checkTabCleanup(); if (state) void run(state); };
  if (typeof GM_getTab === "function") {
    try { GM_getTab(tab => { browserTab = tab; start(); }); }
    catch (e) { console.warn("isutori: tab identity unavailable", e); start(); }
  } else start();
})();

})();
