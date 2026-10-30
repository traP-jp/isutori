// ==UserScript==
// @name         isutori autobench
// @namespace    https://github.com/traP-jp/isutori
// @version      1
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
   * Preparation may select a server, but must not enqueue or change isutori's saved run state.
   * submit must recheck the target and button before registering exactly one job.
   * Keep this object in memory; only Job is saved across page reloads.
   * @typedef {{ target: string, submit: () => Promise<Job> }} Prepared
   */

  /** @typedef {{ passed: boolean, score: number, message: string }} BenchResult */

  const adapter = {
    /**
     * Prepares target selection without registering a job. Loading placeholders
     * are not real options. Selecting a target can decide which server the final
     * measurement uses; select only what TARGET names.
     * Custom controls may select on mousedown, not click; inspect their handlers.
     * Omit `view: window` in synthetic events: Tampermonkey may wrap that Window.
     * role="option" may also mark an empty message. Empty alone does not prove
     * loading, failure, or absence of targets; check the portal's state.
     * @param {string} target the TARGET given to `isutori autobench`
     * @returns {Promise<Prepared>}
     */
    async prepare(target) {
      const select = await waitFor("the server options", () => {
        const select = document.querySelector("select");
        return select && !select.matches(":disabled") && [...select.options].some((o) => o.value && !o.disabled)
          ? select
          : null;
      });
      const options = [...select.options].filter((o) => o.value && !o.disabled);
      // Options read like "1: 192.168.0.11 (203.0.113.11)"; target may be any of those parts.
      /** @param {string} text */
      const matchesTarget = (text) => text.split(/[\s:()]+/).includes(target);
      const selectedText = () => JSON.stringify(document.querySelector("select")?.selectedOptions[0]?.text ?? null);
      const matches = options.filter((o) => matchesTarget(o.text));
      if (matches.length !== 1) {
        throw new Error(
          `target ${JSON.stringify(target)} matches ${matches.length} of: ${options.map((o) => o.text).join(", ")}; selected server: ${selectedText()}`,
        );
      }
      const option = matches[0];
      select.value = option.value;
      select.dispatchEvent(new Event("change", { bubbles: true }));
      const enabledButton = () => {
        const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Enqueue");
        return button && !button.matches(":disabled") && button.getAttribute("aria-disabled") !== "true"
          ? button
          : null;
      };
      try {
        await waitFor(
          `selected server ${JSON.stringify(option.text)}`,
          () => document.querySelector("select")?.value === option.value,
        );
        await waitFor("the enabled Enqueue button", enabledButton);
      } catch (e) {
        throw new Error(`${e.message}; selected server: ${selectedText()}`);
      }

      const checkedButton = () => {
        const current = document.querySelector("select");
        if (
          !current ||
          current.matches(":disabled") ||
          current.value !== option.value ||
          !matchesTarget(current.selectedOptions[0]?.text ?? "")
        ) {
          throw new Error(`selected server changed: expected ${JSON.stringify(option.text)}, got ${selectedText()}`);
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
     * Return the result once this job is in a terminal state and its final-result
     * fields come from the same update; null before then.
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
        /** @type {HTMLElement | null | undefined} */ (
          card("Conclusion")?.querySelector(".card-content")
        )?.innerText.trim() ?? "";
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

  /**
   * Keep the statuses in sync with internal/userscript/identity.go.
   * @typedef {{ expected: string, actual: string, status: "matched" | "mismatched" | "unknown" | "not_checked", error?: string }} Verification
   */

  /** @param {Verification | undefined} v */
  function verificationText(v) {
    /** @param {string | undefined} id */
    const short = (id) => id?.slice(0, 19) || "unavailable";
    return `${v?.status}; expected=${short(v?.expected)} actual=${short(v?.actual)}${v?.error ? `; ${v.error}` : ""}`;
  }

  /** The receivers hash this function's source to identify the running script. */
  function scriptSource() {
    return Function.prototype.toString.call(isutoriScript);
  }

  /** @param {number} ms */
  function sleep(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  /**
   * @template T
   * @param {string} what named in the timeout error
   * @param {() => T | null | undefined} fn
   * @returns {Promise<T>}
   */
  async function waitFor(what, fn, timeoutMs = 60 * 1000) {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const v = fn();
      if (v) return v;
      if (Date.now() > deadline) throw new Error(`timed out after ${timeoutMs / 1000}s waiting for ${what}`);
      await sleep(200);
    }
  }

  /**
   * Posts JSON to a local isutori receiver, which the portal's own origin
   * cannot reach with fetch.
   * @param {string} url
   * @param {unknown} body
   * @param {{ headers?: Record<string, string>, unreachable: string, timedOut: string }} options
   * @returns {Promise<string>} the response body
   */
  function postJSON(url, body, { headers = {}, unreachable, timedOut }) {
    return new Promise((resolve, reject) => {
      GM_xmlhttpRequest({
        method: "POST",
        url,
        headers: { "Content-Type": "application/json", ...headers },
        data: JSON.stringify(body),
        timeout: 10000,
        onload: (res) =>
          res.status >= 200 && res.status < 300
            ? resolve(res.responseText)
            : reject(new Error(`${res.status}: ${res.responseText}`)),
        onerror: () => reject(new Error(unreachable)),
        ontimeout: () => reject(new Error(timedOut)),
        onabort: () => reject(new Error("request aborted")),
      });
    });
  }

  const overlaySelector = "[data-isutori-inspect],[data-isutori-badge]";

  /**
   * @param {"data-isutori-inspect" | "data-isutori-badge"} attribute marks it so that recordings leave it out
   * @param {string} side CSS that pins it to a bottom corner
   * @returns {(text: string) => void}
   */
  function overlay(attribute, side) {
    /** @type {HTMLDivElement | undefined} */
    let element;
    return (text) => {
      if (!element) {
        element = document.createElement("div");
        element.setAttribute(attribute, "");
        element.style.cssText = `position:fixed;${side};bottom:.5rem;z-index:99999;padding:.25rem .5rem;background:#222;color:#fff;font:12px monospace;border-radius:4px;white-space:pre-wrap;max-width:calc(100vw - 1rem);overflow-wrap:anywhere`;
        document.body.append(element);
      }
      element.textContent = text;
    };
  }

  /** @param {Node | null} node */
  function isOverlay(node) {
    const element = node instanceof Element ? node : node?.parentElement;
    return !!element?.closest(overlaySelector);
  }

  /**
   * @typedef {object} Run
   * @property {string} runId
   * @property {string} receiver
   * @property {string} target
   * @property {number} startedAt epoch milliseconds
   * @property {number} [deadline] epoch milliseconds
   * @property {string} [note]
   * @property {boolean} [autoOpened]
   * @property {{order: number, received?: boolean, keep?: boolean, job?: Job}} [tab]
   */

  /**
   * @typedef {Run & ({ phase: "enqueue" | "enqueuing" | "done" | "unreported" } | { phase: "waiting", job: Job })} State
   */

  const showRun = overlay("data-isutori-badge", "left:.5rem");

  /** The run of this tab, kept in sessionStorage so that it survives reloads. */
  const runs = {
    key: "isutori-autobench-v2",
    /** @returns {State | null} */
    load() {
      try {
        return JSON.parse(sessionStorage.getItem(runs.key) ?? "null");
      } catch {
        return null;
      }
    },
    /** @param {State} state */
    save(state) {
      sessionStorage.setItem(runs.key, JSON.stringify(state));
      runs.show(state);
    },
    /** @param {State} state */
    show(state) {
      showRun(`isutori ${state.runId}: ${state.phase}${state.note ? `\n${state.note}` : ""}`);
    },
  };

  /**
   * Records the DOM and the user's actions to the receiver of `isutori inspect`.
   * @param {() => void} onSettled runs whenever a start or a send ends
   */
  function createRecorder(onSettled) {
    const key = "isutori-inspect";
    const maxEventBytes = 2 * 1024 * 1024;
    const maxPendingEvents = 100;
    const maxPendingBytes = 8 * 1024 * 1024;
    const eventTypes = ["click", "input", "change", "visibilitychange"];
    const show = overlay("data-isutori-inspect", "right:.5rem");

    /** @typedef {{ endpoint: string, token: string }} Connection */
    /** @typedef {{ page: string, sequence: number, at: string, url: string, reason: string, target: string, visibility: string, script_id: string, html?: string }} InspectionEvent */

    /**
     * "stopped" is a stop the user asked for, "failed" any other.
     * @type {"idle" | "starting" | "recording" | "stopped" | "failed"}
     */
    let mode = "idle";
    let sending = false;
    /** @type {Connection | undefined} */
    let connection;
    /** @type {{ event: InspectionEvent, bytes: number }[]} */
    const pending = [];
    let pendingBytes = 0;
    let page = "";
    let sequence = 0;
    let lastHTML = "";
    /** @type {Verification | undefined} */
    let identity;
    /** @type {MutationObserver | undefined} */
    let observer;
    /** @type {ReturnType<typeof setInterval> | undefined} */
    let timer;

    /** @param {string} message */
    const status = (message) => show(`isutori recording: ${message}`);

    /** @param {string} text @returns {Connection} */
    function parseConnection(text) {
      const url = new URL(text);
      const token = url.hash.slice(1);
      if (
        url.protocol !== "http:" ||
        !["127.0.0.1", "localhost"].includes(url.hostname) ||
        url.pathname !== "/" ||
        url.username ||
        url.password ||
        url.search ||
        !/^[A-Za-z0-9_-]{20,100}$/.test(token)
      ) {
        throw new Error("Use the loopback connection printed by isutori inspect.");
      }
      return { endpoint: `${url.origin}/api/inspect`, token };
    }

    /** @param {Connection} config @param {"" | "/start"} path @param {unknown} body */
    function send(config, path, body) {
      return postJSON(config.endpoint + path, body, {
        headers: { Authorization: `Bearer ${config.token}` },
        unreachable: "receiver disconnected",
        timedOut: "receiver timed out",
      });
    }

    /** @param {"stopped" | "failed"} next @param {string} message */
    function stop(next, message) {
      mode = next;
      observer?.disconnect();
      clearInterval(timer);
      for (const type of eventTypes) document.removeEventListener(type, onEvent, true);
      window.removeEventListener("pagehide", onEvent, true);
      sessionStorage.removeItem(key);
      status(message);
    }

    function snapshot() {
      const copy = document.body.cloneNode(true);
      if (!(copy instanceof HTMLElement)) throw new Error("cannot copy the document body");
      const originals = document.body.querySelectorAll("input,select,textarea");
      const copies = copy.querySelectorAll("input,select,textarea");
      for (const [index, element] of originals.entries()) {
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
          for (const [i, option] of [...element.options].entries()) {
            clone.options[i].toggleAttribute("selected", option.selected);
          }
        }
      }
      for (const element of copy.querySelectorAll(`script,style,meta,${overlaySelector}`)) element.remove();
      return copy.outerHTML;
    }

    async function flush() {
      if (sending || !connection) return;
      sending = true;
      try {
        while (pending.length) {
          const { event, bytes } = pending[0];
          identity = JSON.parse(await send(connection, "", event));
          pending.shift();
          pendingBytes -= bytes;
          if (mode === "recording") {
            status(`recording; ${verificationText(identity)}; saved ${event.sequence}; pending ${pending.length}`);
          } else if (mode === "stopped") {
            status(`stopped; ${pending.length} event(s) pending`);
          }
        }
      } catch (e) {
        stop(
          "failed",
          `STOPPED: ${e.message}; ${pending.length} unsaved event(s). Keep this page to retry, or reload to discard them.`,
        );
      } finally {
        sending = false;
        onSettled();
      }
    }

    /** @param {string} reason @param {string} [target] */
    function capture(reason, target = "") {
      if (mode !== "recording") return;
      try {
        const html = snapshot();
        /** @type {InspectionEvent} */
        const event = {
          page,
          sequence: ++sequence,
          at: new Date().toISOString(),
          url: location.origin + location.pathname,
          reason,
          target,
          visibility: document.visibilityState,
          script_id: identity?.actual ?? "",
        };
        if (html !== lastHTML) event.html = html;
        const bytes = new TextEncoder().encode(JSON.stringify(event)).length;
        if (bytes > maxEventBytes || pending.length >= maxPendingEvents || pendingBytes + bytes > maxPendingBytes) {
          stop("failed", "STOPPED: recording capacity reached; a state was not captured. Start a new recording.");
          return;
        }
        lastHTML = html;
        pending.push({ event, bytes });
        pendingBytes += bytes;
        void flush();
      } catch (e) {
        stop("failed", `STOPPED: ${e.message}`);
      }
    }

    /** @param {Element} element */
    function describe(element) {
      const path = [];
      for (let node = element; node.parentElement && node !== document.body; node = node.parentElement) {
        path.unshift(`${node.localName}:nth-child(${[...node.parentElement.children].indexOf(node) + 1})`);
      }
      return JSON.stringify({
        path: ["body", ...path].join(" > "),
        tag: element.tagName,
        id: element.id,
        name: element.getAttribute("name"),
        role: element.getAttribute("role"),
        type: element.getAttribute("type"),
      });
    }

    /** @param {Event} event */
    function onEvent(event) {
      const element = event.target instanceof Element ? event.target : null;
      if (isOverlay(element)) return;
      const target = element ? describe(element) : "";
      if (event.type === "pagehide" && mode === "recording" && connection) {
        sessionStorage.setItem(key, JSON.stringify({ ...connection, resume: true }));
      }
      capture(event.type, target);
      if (event.type === "click" || event.type === "input" || event.type === "change") {
        setTimeout(() => capture(`${event.type}:after`, target), 0);
      }
    }

    /** @param {Connection} config */
    async function start(config) {
      const newPage = [...crypto.getRandomValues(new Uint8Array(16))]
        .map((byte) => byte.toString(16).padStart(2, "0"))
        .join("");
      const info = JSON.parse(await send(config, "/start", { page: newPage, source: scriptSource() }));
      if (info.origin !== location.origin)
        throw new Error(`Expected portal origin ${info.origin}, got ${location.origin}`);
      connection = config;
      if (pending.length) {
        await flush();
        if (pending.length) {
          throw new Error("Previous events could not be saved; receiver must be restored before restarting.");
        }
      }
      page = newPage;
      identity = info.userscript;
      sequence = 0;
      lastHTML = "";
      sessionStorage.setItem(key, JSON.stringify({ ...config, resume: false }));
      mode = "recording";
      status(`recording; ${verificationText(identity)}`);
      capture("start");
      if (mode !== "recording") return;
      observer = new MutationObserver((records) => {
        if (records.some((record) => !isOverlay(record.target))) capture("mutation");
      });
      observer.observe(document.body, { subtree: true, childList: true, attributes: true, characterData: true });
      for (const type of eventTypes) document.addEventListener(type, onEvent, true);
      window.addEventListener("pagehide", onEvent, true);
      timer = setInterval(() => capture("tick"), 1000);
    }

    /**
     * @param {() => Connection | undefined} connect undefined starts nothing
     * @param {(error: Error) => void} fail
     */
    async function begin(connect, fail) {
      mode = "starting";
      try {
        const config = connect();
        if (config) await start(config);
      } catch (e) {
        fail(e);
      } finally {
        if (mode === "starting") mode = "idle";
        onSettled();
      }
    }

    function registerMenus() {
      GM_registerMenuCommand("isutori: start recording", () => {
        if (mode === "starting" || mode === "recording" || sending) {
          return alert("Recording is active or a request is still pending.");
        }
        const value = prompt("Paste the connection printed by isutori inspect:");
        if (!value) return;
        return begin(
          () => {
            const config = parseConnection(value.trim());
            const elsewhere =
              connection && (config.endpoint !== connection.endpoint || config.token !== connection.token);
            if (pending.length && elsewhere) {
              throw new Error("Unsaved events belong to the previous receiver; reload to discard them explicitly.");
            }
            return config;
          },
          (e) => status(`STOPPED: ${e.message}`),
        );
      });
      GM_registerMenuCommand("isutori: stop recording", () => {
        if (mode === "starting") return alert("Recording is starting; try stopping after it connects.");
        if (mode !== "recording") return;
        capture("stop");
        stop("stopped", `stopped; ${pending.length} event(s) pending`);
      });
    }

    function resume() {
      let saved;
      try {
        saved = sessionStorage.getItem(key);
      } catch {
        return;
      }
      if (!saved) return;
      mode = "starting";
      void Promise.resolve().then(() =>
        begin(
          () => {
            const config = JSON.parse(saved);
            const navigation = performance.getEntriesByType("navigation")[0];
            const reloaded = navigation instanceof PerformanceNavigationTiming && navigation.type === "reload";
            if (!config.resume && !reloaded) {
              sessionStorage.removeItem(key);
              return undefined;
            }
            return parseConnection(`${config.endpoint.replace(/\/api\/inspect$/, "/")}#${config.token}`);
          },
          (e) => stop("failed", `STOPPED: ${e.message}`),
        ),
      );
    }

    return {
      registerMenus,
      resume,
      busy: () => mode === "starting" || mode === "recording" || sending || pending.length > 0,
    };
  }

  /**
   * Closes this tab once a newer run of the same group has reported its result,
   * if the user turned automatic cleanup on.
   * @param {() => boolean} busy whether closing would lose a recording
   */
  function createTabCleanup(busy) {
    const enabledKey = "isutori-tab-cleanup";
    /** @typedef {{runId: string, order: number, receivedOrder: number}} TabGroup */
    const available = typeof GM_getValue === "function";
    /** @type {{isutoriRun?: string} | undefined} */
    let browserTab;
    let launchedFromHash = false;

    /** @param {State} state */
    const groupKey = (state) => `isutori-tabs:${location.origin}:${new URL(state.receiver).origin}`;
    /** @param {State} state */
    const group = (state) => /** @type {TabGroup | null} */ (GM_getValue(groupKey(state), null));
    /** @param {State} state */
    const ownsRun = (state) => browserTab?.isutoriRun === state.runId;

    /**
     * Gives this tab the next order of its group. Tabs opened by hand, or that
     * only copied the run state, stay out of the group.
     * @param {State} state @returns {State}
     */
    function register(state) {
      if (!available || !state.autoOpened || state.tab || !browserTab) return state;
      if (!launchedFromHash && !ownsRun(state)) return state;
      try {
        const latest = group(state);
        const order = latest?.runId === state.runId ? latest.order : (latest?.order ?? 0) + 1;
        browserTab.isutoriRun = state.runId;
        GM_saveTab(browserTab);
        /** @type {State} */
        const registered = { ...state, tab: { order } };
        runs.save(registered);
        if (latest?.runId !== state.runId) {
          GM_setValue(groupKey(state), { runId: state.runId, order, receivedOrder: latest?.receivedOrder ?? 0 });
        }
        return registered;
      } catch (e) {
        console.warn("isutori: tab cleanup unavailable", e);
        return state;
      }
    }

    function check() {
      try {
        const state = runs.load();
        if (!state?.autoOpened || !ownsRun(state) || state.phase !== "done") return;
        const tab = state.tab;
        if (!tab?.received || tab.keep || busy() || !available || !GM_getValue(enabledKey, false)) return;
        const current = adapter.currentJob();
        if (!current || current.jobId !== tab.job?.jobId) return;
        const latest = group(state);
        if (latest && latest.receivedOrder > tab.order) window.close();
      } catch (e) {
        console.warn("isutori: could not close old tab", e);
      }
    }

    /** @param {State} state */
    function notifyReceived(state) {
      if (!available || !state.tab?.received) return;
      try {
        const latest = group(state);
        if (latest && latest.order >= state.tab.order && latest.receivedOrder < state.tab.order) {
          GM_setValue(groupKey(state), { ...latest, receivedOrder: state.tab.order });
        }
      } catch (e) {
        console.warn("isutori: tab cleanup notification failed", e);
      }
    }

    function registerMenus() {
      GM_registerMenuCommand("isutori: toggle automatic tab cleanup", () => {
        if (!available)
          return alert("Tab cleanup requires Tampermonkey storage permissions; reinstall the userscript.");
        try {
          const enabled = !GM_getValue(enabledKey, false);
          GM_setValue(enabledKey, enabled);
          alert(`Automatic tab cleanup ${enabled ? "enabled" : "disabled"}.`);
          check();
        } catch (e) {
          alert(`Tab cleanup unavailable: ${e.message}`);
        }
      });
      GM_registerMenuCommand("isutori: toggle keep this tab", () => {
        const state = runs.load();
        if (!state?.tab || !ownsRun(state)) return alert("This tab is not managed by automatic tab cleanup.");
        const keep = !state.tab.keep;
        runs.save({ ...state, tab: { ...state.tab, keep } });
        alert(keep ? "This tab will be kept." : "This tab can be cleaned up.");
        check();
      });
    }

    /**
     * Calls ready once this tab's identity is known.
     * @param {State | null} state @param {boolean} fromHash whether autobench just opened this tab
     * @param {() => void} ready
     */
    function attach(state, fromHash, ready) {
      launchedFromHash = fromHash;
      try {
        if (typeof GM_addValueChangeListener === "function") {
          GM_addValueChangeListener(enabledKey, check);
          if (state) GM_addValueChangeListener(groupKey(state), check);
        }
      } catch (e) {
        console.warn("isutori: tab cleanup listener unavailable", e);
      }
      if (typeof GM_getTab !== "function") return ready();
      try {
        GM_getTab((tab) => {
          browserTab = tab;
          ready();
        });
      } catch (e) {
        console.warn("isutori: tab identity unavailable", e);
        ready();
      }
    }

    return { register, check, notifyReceived, registerMenus, attach };
  }

  /**
   * Runs the benchmark that `isutori autobench` opened this tab for, and
   * reports its result to the receiver.
   * @param {ReturnType<typeof createTabCleanup>} tabs
   */
  function createAutobench(tabs) {
    const pollMs = 2000;
    // Used when the CLI passed no deadline (an older isutori, or no ISUTORI_CONFIG).
    const resultTimeoutMs = 20 * 60 * 1000;
    /** @type {Verification | undefined} */
    let verified;

    /** @returns {{ state: State | null, fromHash: boolean }} */
    function initialState() {
      const params = new URLSearchParams(location.hash.slice(1));
      const runId = params.get("autobench_v2");
      const receiver = params.get("receiver");
      const target = params.get("target");
      if (!runId || !receiver || !target) return { state: runs.load(), fromHash: false };
      history.replaceState(null, "", location.pathname + location.search);
      const current = runs.load();
      if (current?.runId === runId) return { state: current, fromHash: true };
      const deadline = Number(params.get("deadline"));
      return {
        state: {
          runId,
          receiver,
          target,
          phase: "enqueue",
          startedAt: Date.now(),
          ...(Number.isFinite(deadline) && deadline > 0 ? { deadline } : {}),
          autoOpened: true,
        },
        fromHash: true,
      };
    }

    /** @param {State} state @param {"/api/userscript" | "/api/result" | "/api/heartbeat"} path @param {object} body */
    function post(state, path, body) {
      return postJSON(
        `${state.receiver}${path}`,
        { run_id: state.runId, ...body },
        { unreachable: `cannot reach ${state.receiver}`, timedOut: `timed out posting to ${state.receiver}` },
      );
    }

    /**
     * The saved tab record, which the keep menu may have changed during the run.
     * @param {State} state
     */
    function latestTab(state) {
      const saved = runs.load();
      return saved?.runId === state.runId ? saved.tab : state.tab;
    }

    /**
     * @param {State} state
     * @param {BenchResult | { error: string }} body error when no result could be obtained
     */
    async function report(state, body) {
      try {
        await post(state, "/api/result", { ...body, userscript: verified });
      } catch (e) {
        runs.save({
          ...state,
          tab: latestTab(state),
          phase: "unreported",
          note: `could not report to isutori: ${e.message}`,
        });
        return;
      }
      const tab = latestTab(state);
      /** @type {State} */
      const done = {
        ...state,
        phase: "done",
        tab: tab && { ...tab, received: !("error" in body), job: "job" in state ? state.job : undefined },
        note: "error" in body ? body.error : `passed=${body.passed} score=${body.score}`,
      };
      runs.save(done);
      tabs.notifyReceived(done);
      tabs.check();
    }

    /** @param {State} state @returns {Promise<State | null>} null when the script does not match */
    async function verify(state) {
      try {
        verified = JSON.parse(await post(state, "/api/userscript", { source: scriptSource() }));
        if (verified?.status !== "matched") throw new Error(verificationText(verified));
      } catch (e) {
        runs.save({
          ...state,
          note: `BLOCKED: userscript verification: ${e.message}; use the manual result form or install the expected script and reload`,
        });
        return null;
      }
      if (!state.note) return state;
      /** @type {State} */
      const cleared = { ...state, note: undefined };
      runs.save(cleared);
      return cleared;
    }

    /**
     * Registers the job, or after a reload while registering, finds it.
     * @param {State} state @returns {Promise<State | null>} the waiting state, or null once reported
     */
    async function enqueue(state) {
      if (state.phase === "enqueue") {
        try {
          const prepared = await adapter.prepare(state.target);
          runs.save({ ...state, phase: "enqueuing" });
          const job = await prepared.submit();
          return { ...state, phase: "waiting", job };
        } catch (e) {
          await report(state, { error: `enqueue: ${e.message}` });
          return null;
        }
      }
      // Never resubmit after a reload: registration may already have succeeded.
      try {
        const job = await waitFor("the job page", () => adapter.currentJob(), 15 * 1000);
        return { ...state, phase: "waiting", job };
      } catch {
        await report(state, { error: "page was reloaded while enqueuing and no job page followed; check the portal" });
        return null;
      }
    }

    /** @param {State} state */
    async function run(state) {
      if (state.phase === "done" || state.phase === "unreported") return runs.show(state);
      if (state.phase === "enqueue") runs.save(state);
      else runs.show(state);
      const verifiedState = await verify(state);
      if (!verifiedState) return;
      let current = tabs.register(verifiedState);
      if (current.phase !== "waiting") {
        const waiting = await enqueue(current);
        if (!waiting) return;
        runs.save(waiting);
        current = waiting;
      }
      if (current.phase !== "waiting") return;
      for (;;) {
        let result;
        try {
          result = adapter.readResult(current.job);
        } catch (e) {
          return report(current, { error: `readResult: ${e.message}` });
        }
        if (result) return report(current, result);
        if (Date.now() > (current.deadline ?? current.startedAt + resultTimeoutMs)) {
          return report(current, { error: "timed out waiting for the result" });
        }
        // Lets the CLI warn when the tab goes silent; a failed beat must not stop result collection.
        void post(current, "/api/heartbeat", {}).catch(() => {});
        await sleep(pollMs);
      }
    }

    function registerMenus() {
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
        const state = runs.load();
        if (state && state.phase !== "done") {
          return alert(`This tab has run ${state.runId} (${state.phase}). Open a separate portal tab for the check.`);
        }
        const target = prompt("Target server (selection will change; no benchmark will be enqueued):")?.trim();
        if (!target) return;
        checking = true;
        try {
          const prepared = await adapter.prepare(target);
          alert(
            JSON.stringify({ requested: target, selected: prepared.target, ready: true, enqueued: false }, null, 2),
          );
        } catch (e) {
          alert(`prepare: target ${JSON.stringify(target)}: ${e.message}`);
        } finally {
          checking = false;
        }
      });
    }

    return { initialState, run, registerMenus };
  }

  const tabs = createTabCleanup(() => recorder.busy());
  const recorder = createRecorder(() => tabs.check());
  const autobench = createAutobench(tabs);
  recorder.registerMenus();
  autobench.registerMenus();
  tabs.registerMenus();
  recorder.resume();
  const { state, fromHash } = autobench.initialState();
  tabs.attach(state, fromHash, () => {
    tabs.check();
    if (state) void autobench.run(state);
  });
})();
