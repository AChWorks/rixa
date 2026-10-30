// SPDX-License-Identifier: MPL-2.0
package product

// This controller owns only Rixa draft forms. It consumes the shared Identity
// CSRF and browser localization APIs; it does not replace either mechanism.
const draftRecoveryJS = `
"use strict";
(() => {
  const root = document.querySelector("[data-content-workspace]");
  if (!root) return;
  const forms = [...root.querySelectorAll("form[data-draft-form]")];
  if (!forms.length) return;
  const i18n = window.AChrixI18n;
  const text = key => i18n.text("rixa", key);
  const status = document.getElementById("status");
  const recovery = root.querySelector("[data-draft-recovery]");
  const session = root.querySelector("[data-session-recovery]");
  const conflict = root.querySelector("[data-conflict-recovery]");
  const editor = root.querySelector("[data-editor]");
  const authPath = document.body.dataset.authPath;
  let busy = false;
  let pending = null;
  let baseline = fingerprint();
  function fingerprint() {
    return JSON.stringify(forms.map(form => [...form.elements].filter(c => ["title","body_html","kind"].includes(c.name)).map(c => [c.name,c.value])));
  }
  function dirty() { return fingerprint() !== baseline; }
  function announce(key, failure = false) {
    status.textContent = text(key);
    status.setAttribute("role", failure ? "alert" : "status");
    status.focus();
  }
  function lock() {
    const held = busy || !!pending;
    forms.forEach(form => {
      form.setAttribute("aria-busy", String(busy));
      [...form.elements].forEach(control => { control.disabled = held; });
    });
    if (editor) editor.contentEditable = held ? "false" : "true";
    root.querySelectorAll("[data-wrap],[data-block],[data-add-link],[data-add-media]").forEach(button => { button.disabled = held; });
    document.querySelectorAll("form[data-admin-form]:not([data-read-only]) button[type=submit]").forEach(button => {
      button.disabled = held || button.dataset.denied === "true";
    });
    recovery.querySelectorAll("button").forEach(button => { button.disabled = busy; });
  }
  function snapshot(form) {
    const values = {};
    for (const control of form.elements) {
      if (control.name) values[control.name] = control.value;
    }
    return Object.freeze({
      form,
      body: JSON.stringify(values),
      operation: values.operation_id,
      kind: form.hasAttribute("data-content-create") ? "content.create" : "content.save",
      resource: values.id || "",
      expected: values.expected_head || ""
    });
  }
  function validID(value) { return typeof value === "string" && /^[A-Z2-7]{26}$/.test(value); }
  function validRevision(value) {
    return typeof value === "string" && /^[1-9][0-9]{0,18}$/.test(value) &&
      (value.length < 19 || value <= "9223372036854775807");
  }
  function laterRevision(value, before) {
    return validRevision(value) && validRevision(before) &&
      (value.length > before.length || value.length === before.length && value > before);
  }
  function matchesScope(result) {
    return !root.dataset.siteId || result.site_id === root.dataset.siteId && result.origin === root.dataset.origin;
  }
  function matchesResult(attempt, result, inspection) {
    if (!result || !matchesScope(result) || result.operation_id !== attempt.operation) return false;
    const id = inspection ? result.resource_id : result.id;
    const revision = inspection ? result.result_revision : result.revision;
    if (!validID(id) || !validRevision(revision)) return false;
    if (inspection && result.kind !== attempt.kind) return false;
    return attempt.kind === "content.create" ? revision === "1" :
      id === attempt.resource && laterRevision(revision, attempt.expected);
  }
  async function request(path, body, readOnly) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 15000);
    let sent = false;
    try {
      const csrfResponse = await fetch(authPath + "/csrf", {
        mode:"same-origin",redirect:"error",referrer:location.origin + "/",
        referrerPolicy:"same-origin",credentials:"same-origin",cache:"no-store",
        headers:{"X-Identity-Request":"1"},signal:controller.signal
      });
      if (!csrfResponse.ok) throw {status:csrfResponse.status};
      const token = await csrfResponse.json();
      if (typeof token.csrf !== "string") throw new Error("unavailable");
      sent = true;
      const response = await fetch(path, {
        method:"POST",mode:"same-origin",redirect:"error",credentials:"same-origin",cache:"no-store",
        headers:{"Content-Type":"application/json","X-Identity-Request":"1","X-CSRF-Token":token.csrf},
        body,signal:controller.signal
      });
      let result;
      try { result = await response.json(); } catch (_) { /* Unknown writes remain unresolved. */ }
      if (!response.ok) throw {status:response.status,code:result?.code};
      if (!result) throw new Error("unavailable");
      return result;
    } catch (error) {
      throw {status:error.status || 0,code:error.code || "",
        unknown:!readOnly && sent && ![400,401,403,404,409,413,429].includes(error.status)};
    } finally { clearTimeout(timer); }
  }
  function hold(attempt) {
    pending = attempt;
    recovery.hidden = false;
    recovery.querySelector("[data-pending-operation]").textContent = attempt.operation;
    recovery.querySelector("[data-recovery-message]").textContent = text("draft_unknown");
    const lookup = root.querySelector("[data-operation-lookup-input]");
    if (lookup) lookup.value = attempt.operation;
    lock();
  }
  function errorMessage(error, readOnly = false) {
    if (error.status === 401) {
      session.hidden = false;
      announce("draft_session_expired", true);
      return;
    }
    if (readOnly) {
      announce(error.status === 404 ? "draft_inspection_missing" : "draft_inspection_failed", true);
      return;
    }
    if (error.status === 409) {
      conflict.hidden = false;
      const latest = conflict.querySelector("[data-latest-link]");
      if (latest && pending?.resource) latest.href = root.dataset.base + "/edit/" + pending.resource;
      announce("draft_conflict", true);
      return;
    }
    announce(({400:"draft_invalid",403:"draft_denied",404:"draft_missing",413:"draft_invalid",429:"draft_limited"})[error.status] || "draft_unavailable", true);
  }
  function accept(attempt, result, inspection) {
    if (!matchesResult(attempt,result,inspection)) {
      hold(attempt);
      announce("draft_result_mismatch", true);
      return;
    }
    pending = null;
    recovery.hidden = true;
    conflict.hidden = true;
    session.hidden = true;
    lock();
    // Existing Rixa editor behavior advances exact decimal fields and rotates
    // identity only after an acknowledged or positively reconciled result.
    attempt.form.dispatchEvent(new CustomEvent("admin:success", {
      detail:inspection ? {id:result.resource_id,revision:result.result_revision,operation_id:result.operation_id} : result,
      bubbles:true
    }));
    baseline = fingerprint();
    announce("draft_saved");
  }
  async function send(attempt) {
    if (busy) return;
    busy = true; lock(); announce("draft_working");
    try {
      const result = await request(attempt.form.action, attempt.body, false);
      accept(attempt,result,false);
    } catch (error) {
      if (error.unknown) {
        hold(attempt);
        announce("draft_unknown", true);
      } else {
        // A rejected retry cannot prove the original uncertain request absent.
        errorMessage(error);
      }
    } finally { busy = false; lock(); }
  }
  forms.forEach(form => form.addEventListener("submit", event => {
    event.preventDefault();
    if (busy || pending) return;
    if (!form.reportValidity()) return;
    const latest = conflict.querySelector("[data-latest-link]");
    const id = form.querySelector("input[name=id]")?.value;
    if (latest && id) latest.href = root.dataset.base + "/edit/" + id;
    send(snapshot(form));
  }));
  root.querySelector("[data-inspect-pending]").addEventListener("click", async () => {
    if (busy || !pending) return;
    const attempt = pending;
    busy = true; lock(); announce("draft_working");
    try {
      const result = await request(root.dataset.base + "/operation", JSON.stringify({operation_id:attempt.operation}), true);
      accept(attempt,result,true);
    } catch (error) { errorMessage(error,true); }
    finally { busy = false; lock(); }
  });
  root.querySelector("[data-retry-pending]").addEventListener("click", () => {
    // The retained serialized body is immutable. Never rebuild it from a form,
    // replace the operation ID, or infer absence from a failed inspection.
    if (pending && !busy) send(pending);
  });
  function mayLeave() {
    return !busy && (!(dirty() || pending) || window.confirm(text("draft_navigation_loss")));
  }
  document.addEventListener("click", event => {
    const link = event.target.closest("a[href]");
    const logout = event.target.closest("#logout");
    if (!logout && (!link || link.target === "_blank" || link.getAttribute("href").startsWith("#") || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey)) return;
    if (!mayLeave()) { event.preventDefault(); event.stopImmediatePropagation(); }
  },true);
  document.addEventListener("submit", event => {
    const form = event.target;
    if (form.matches("[data-draft-form]")) return;
    if (form.action?.includes("/admin/language/")) {
      if (!mayLeave()) { event.preventDefault();event.stopImmediatePropagation(); }
    } else if ((busy || pending) && !form.hasAttribute("data-read-only")) {
      event.preventDefault();event.stopImmediatePropagation();announce("draft_unknown",true);
    }
  },true);
  window.addEventListener("beforeunload", event => {
    if (busy || pending || dirty()) { event.preventDefault(); event.returnValue = ""; }
  });
  // Generic read-only operation inspection remains independent; its failure is
  // never promoted to a new unknown write, and cannot unlock a pending mutation.
  document.addEventListener("admin:success", () => queueMicrotask(lock));
  document.addEventListener("admin:failure", () => queueMicrotask(lock));
  lock();
})();
`
