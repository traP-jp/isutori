// ==UserScript==
// @name         isutori autobench
// @namespace    https://github.com/traP-jp/isutori
// @version      4
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

  /**
   * Identifies a job. It is kept in sessionStorage, so it must survive JSON.
   * @typedef {{ jobId: string }} Job
   */

  const adapter = {
    /**
     * Starts exactly one benchmark job against target on the page that isutori
     * opened. The portal may take the last target for the final measurement,
     * so never fall back to another server.
     * @param {string} target the TARGET given to `isutori autobench`
     * @returns {Promise<Job>}
     * @throws if target is not offered or the portal refuses, e.g. because
     *   another job is running.
     */
    async enqueue(target) {
      const select = await waitFor("the server select", () => document.querySelector("select"));
      // Options read like "1: 192.168.0.11 (203.0.113.11)"; target may be any of those parts.
      const matches = [...select.options].filter((o) => o.text.split(/[\s:()]+/).includes(target));
      if (matches.length !== 1) {
        throw new Error(`target ${JSON.stringify(target)} matches ${matches.length} of: ${[...select.options].map((o) => o.text).join(", ")}`);
      }
      select.value = matches[0].value;
      select.dispatchEvent(new Event("change", { bubbles: true }));
      const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Enqueue");
      if (!button) throw new Error("Enqueue button not found");
      button.click();
      return waitFor("the job page", () => {
        const job = adapter.currentJob();
        if (job) return job;
        const error = document.querySelector(".message.is-danger, .notification.is-danger");
        if (error) throw new Error(`portal refused to enqueue: ${error.textContent?.trim()}`);
        return null;
      });
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

  /**
   * What is posted to the isutori receiver along with the run id.
   * @typedef {BenchResult | { error: string }} Report
   */

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

  /**
   * Each tab tracks at most one run; reopening the same run id resumes it.
   * @returns {State | null}
   */
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
      saveState({ ...state, phase: "enqueuing" });
      try {
        const job = await adapter.enqueue(state.target);
        state = { ...state, phase: "waiting", job };
        saveState(state);
      } catch (e) {
        return report(state, { error: `enqueue: ${e.message}` });
      }
    } else if (state.phase === "enqueuing") {
      // The page was reloaded while enqueuing. Never press the button again:
      // either this page shows the new job or the outcome is unknown.
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

  const state = stateFromHash() ?? loadState();
  if (state) run(state);
})();
