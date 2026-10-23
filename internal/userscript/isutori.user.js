// ==UserScript==
// @name         isutori autobench
// @namespace    https://github.com/traP-jp/isutori
// @version      6
// @description  Enqueues a benchmark when opened by `isutori bench` and reports the result to it.
// @match        http://127.0.0.1:7879/*
// @grant        GM_xmlhttpRequest
// @grant        GM_registerMenuCommand
// @connect      127.0.0.1
// @connect      localhost
// @run-at       document-idle
// ==/UserScript==

// @ts-check

(function () {
  "use strict";
  if (typeof window !== "undefined" && window.top !== window.self) return;

  const key = "isutori-inspect";
  /** @typedef {{ endpoint: string, token: string }} Connection */
  /** @typedef {{ page: string, sequence: number, at: string, url: string, reason: string, target: string, visibility: string, html?: string }} InspectionEvent */
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
  let active = false;
  let stoppedByUser = false;
  const eventTypes = ["click", "input", "change", "visibilitychange"];

  /** @param {string} message */
  function show(message) {
    if (!status) {
      status = document.createElement("div");
      status.setAttribute("data-isutori-inspect", "");
      status.style.cssText = "position:fixed;right:.5rem;bottom:.5rem;z-index:99999;background:#222;color:#fff;padding:.5rem;font:12px monospace";
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

  /** @param {Connection} config @param {InspectionEvent} [event] @returns {Promise<string>} */
  function request(config, event) {
    return new Promise((resolve, reject) => {
      GM_xmlhttpRequest({
        method: event ? "POST" : "GET", url: config.endpoint,
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${config.token}` },
        data: event ? JSON.stringify(event) : undefined,
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
        await request(connection, event);
        pending.shift();
        pendingBytes -= new TextEncoder().encode(JSON.stringify(event)).length;
        if (active) show(`recording; saved ${event.sequence}; pending ${pending.length}`);
        else if (stoppedByUser) show(`stopped; ${pending.length} event(s) pending`);
      }
    } catch (e) {
      stop(`STOPPED: ${e.message}; ${pending.length} unsaved event(s). Keep this page to retry, or reload to discard them.`);
    } finally {
      sending = false;
    }
  }

  /** @param {string} reason @param {string} [target] */
  function capture(reason, target = "") {
    if (!active) return;
    try {
      const html = snapshot();
      /** @type {InspectionEvent} */
      const event = { page, sequence: ++sequence, at: new Date().toISOString(), url: location.origin + location.pathname, reason, target, visibility: document.visibilityState };
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
    const info = JSON.parse(await request(config));
    if (info.origin !== location.origin) throw new Error(`Expected portal origin ${info.origin}, got ${location.origin}`);
    connection = config;
    if (pending.length) {
      await flush();
      if (pending.length) throw new Error("Previous events could not be saved; receiver must be restored before restarting.");
    }
    page = [...crypto.getRandomValues(new Uint8Array(16))].map((byte) => byte.toString(16).padStart(2, "0")).join("");
    sequence = 0;
    lastHTML = "";
    sessionStorage.setItem(key, JSON.stringify({ ...config, resume: false }));
    active = true;
    stoppedByUser = false;
    show("recording");
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
    }).catch((e) => stop(`STOPPED: ${e.message}`)).finally(() => { starting = false; });
  }
})();

(function () {
  "use strict";

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
     * @param {Job | null} job null reads whatever job the current page shows.
     * @returns {BenchResult | null} null while the job is still running.
     */
    readResult(job) {
      if (job && location.pathname !== `/contestant/benchmark_jobs/${job.jobId}`) {
        throw new Error(`left the job page: ${location.pathname}`);
      }
      /** @param {string} title */
      const card = (title) =>
        [.../** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll(".card"))].find(
          (c) => c.querySelector(".card-header-title")?.textContent?.trim() === title,
        );
      const summary = card("Summary");
      if (!summary) return null;
      const status = summary.querySelector(".tag")?.textContent?.trim();
      const conclusion =
        /** @type {HTMLElement | null | undefined} */ (card("Conclusion")?.querySelector(".card-content"))?.innerText.trim() ?? "";
      if (status === "ERRORED") {
        return { passed: false, score: 0, message: `ERRORED\n${conclusion}`.trim() };
      }
      if (status !== "FINISHED") return null;
      const result = card("Result");
      const tags = [...(result?.querySelectorAll(".tag") ?? [])].map((t) => t.textContent?.trim());
      const score = result?.innerText.match(/Score:\s*(-?\d+)/);
      if (!tags.includes("Finished") || !score) return null;
      return { passed: tags.includes("Passed"), score: Number(score[1]), message: conclusion };
    },
  };
  // ===== END OF PORTAL ADAPTER =====

  /** @typedef {{ passed: boolean, score: number, message: string }} BenchResult */

  /** @typedef {BenchResult | { error: string }} Report */

  /**
   * @typedef {object} Run
   * @property {string} runId
   * @property {string} receiver
   * @property {string} target
   * @property {number} startedAt epoch milliseconds
   * @property {string} [note]
   */

  /**
   * @typedef {Run & ({ phase: "enqueue" | "enqueuing" | "done" | "unreported" } | { phase: "waiting", job: Job })} State
   */

  const pollMs = 2000;
  const enqueueTimeoutMs = 60 * 1000;
  const resultTimeoutMs = 20 * 60 * 1000;
  const storageKey = "isutori-autobench";

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
    const runId = params.get("autobench");
    const receiver = params.get("receiver");
    const target = params.get("target");
    if (!runId || !receiver || !target) return null;
    history.replaceState(null, "", location.pathname + location.search);
    const current = loadState();
    if (current?.runId === runId) return current;
    return { runId, receiver, target, phase: "enqueue", startedAt: Date.now() };
  }

  /**
   * @param {State} state
   * @param {Report} body
   * @returns {Promise<void>}
   */
  function post(state, body) {
    return new Promise((resolve, reject) => {
      GM_xmlhttpRequest({
        method: "POST",
        url: `${state.receiver}/api/result`,
        headers: { "Content-Type": "application/json" },
        data: JSON.stringify({ run_id: state.runId, ...body }),
        onload: (res) =>
          res.status >= 200 && res.status < 300 ? resolve() : reject(new Error(`${res.status} ${res.responseText}`)),
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
      await post(state, body);
      saveState({ ...state, phase: "done", note: "error" in body ? body.error : `passed=${body.passed} score=${body.score}` });
    } catch (e) {
      saveState({ ...state, phase: "unreported", note: `could not report to isutori: ${e.message}` });
    }
  }

  /** @param {State} state */
  async function run(state) {
    if (state.phase === "enqueue") {
      saveState(state);
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
        "background:#222;color:#fff;font:12px monospace;border-radius:4px;white-space:pre";
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

  const state = stateFromHash() ?? loadState();
  if (state) run(state);
})();
