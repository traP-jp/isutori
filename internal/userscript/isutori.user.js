// ==UserScript==
// @name         isutori autobench
// @namespace    https://github.com/traP-jp/isutori
// @version      5
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
