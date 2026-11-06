// SPDX-License-Identifier: MPL-2.0
"use strict";

const fs = require("node:fs/promises");
const https = require("node:https");
const path = require("node:path");
const {chromium} = require("playwright");

// Real Chromium + real Rixa/Identity/PostgreSQL, but only against a new,
// owner-private loopback fixture created by TestControlAccountBrowserFixture.
async function main() {
  const root = process.argv[2];
  if (!root || !path.isAbsolute(root) || path.normalize(root) !== root || root === "/")
    throw new Error("absolute new private fixture directory required");
  const stat = await fs.lstat(root);
  if (!stat.isDirectory() || stat.isSymbolicLink() || (stat.mode & 0o077) !== 0 || await fs.realpath(root) !== root)
    throw new Error("fixture directory must be ordinary and private");
  const fixture = JSON.parse(await fs.readFile(path.join(root, "ready.json"), "utf8"));
  const check = (condition, message) => { if (!condition) throw new Error(message); };
  check(fixture.schema === "rixa.control-account-browser.v1" && /^[0-9a-f]{64}$/.test(fixture.binary_sha256),
    "wrong fixture identity");
  for (const [key, host] of [["control","control.localhost"],["site_a","a.localhost"],["site_b","b.localhost"]]) {
    const address = new URL(fixture[key]);
    check(address.protocol === "https:" && address.hostname === host && address.port &&
      address.pathname === "/" && !address.search && !address.hash &&
      address.origin === fixture[key], "refusing nonlocal fixture URL");
  }
  const checks = [];
  const errors = [];
  const browser = await chromium.launch({headless: true});
  let context;
  try {
    context = await browser.newContext({ignoreHTTPSErrors: true, viewport:{width:1366,height:900}});
    const page = await context.newPage();
    page.setDefaultTimeout(12000);
    page.on("pageerror", error => errors.push(error.message));
    // Independent BrowserContext observation remains active after fault
    // injection. Even a delayed HTTP 409 replay counts as an attempt.
    const accountMutations = [], observationErrors = [];
    context.on("request", request => {
      try {
        if (request.method() !== "POST") return;
        const target = new URL(request.url());
        if (target.origin !== fixture.control || target.pathname !== "/admin/sites/account-create") return;
        const submitted = JSON.parse(request.postData());
        check(["browser-account-a","browser-account-b"].includes(submitted.login) &&
          ["site-a","site-b"].includes(submitted.site_id), "unexpected account mutation in Browser transport");
        accountMutations.push({login:submitted.login, site:submitted.site_id});
      } catch (error) { observationErrors.push(error.message); }
    });
    const assertMutations = () => {
      check(observationErrors.length === 0, "account transport observation failed: " + observationErrors.join("; "));
      const actual = accountMutations.map(m => m.site + "/" + m.login);
      check(JSON.stringify(actual) === JSON.stringify([
        "site-a/browser-account-a",
        "site-a/browser-account-a",
        "site-b/browser-account-b"
      ]), "browser account mutation replay or missing/foreign attempt: " + actual.join(", "));
    };
    const notice = async (key, role) => {
      const expected = await page.evaluate(key => window.AChrixI18n.text("admin", key), key);
      const current = await page.locator("#status").textContent();
      check(typeof expected === "string" && expected.trim().length > 3 &&
        current.trim() === expected.trim() &&
        await page.locator("#status").getAttribute("role") === role,
        "visible localized status did not match " + key + "/" + role);
    };
    const record = label => checks.push(label);
    const login = async () => {
      const response = await page.goto(fixture.control + "/admin/login");
      check(response?.status() === 200, "Control login unavailable");
      await page.locator("form[data-login] input[name=login]").fill("control-admin");
      await page.locator("form[data-login] input[name=password]").fill("A-Strong-Control-Password-2026!");
      await Promise.all([
        page.waitForURL(url => url.origin === fixture.control && url.pathname !== "/admin/login"),
        page.locator("form[data-login] button[type=submit]").click()
      ]);
      await page.locator("#logout").waitFor();
      const controlCookie = (await context.cookies(fixture.control))
        .find(cookie => cookie.name === "__Host-AChrix-Session");
      check(controlCookie && controlCookie.secure && controlCookie.httpOnly &&
        controlCookie.sameSite === "Strict" && controlCookie.path === "/",
        "Control session cookie lacks Secure/HttpOnly/SameSite/Host scope");
      check((await context.cookies(fixture.site_a)).length === 0 &&
        (await context.cookies(fixture.site_b)).length === 0, "Control login became a site login");
    };
    await login();
    record("control-login-cookie-scope");

    await page.goto(fixture.control + "/admin/sites/");
    for (const site of ["site-a","site-b"]) {
      check(await page.locator('a[href="/admin/sites/account-create/' + site + '"]').count() === 1,
        "Site inventory has missing/repeated account action " + site);
    }
    check(await page.locator("form[data-control-account-create]").count() === 0,
      "Site inventory contains eager account form");
    check(await page.locator('a[href="/admin/sites/account-create/disabled"]').count() === 0,
      "disabled Site exposed create action");
    record("site-list-explicit-selection");

    async function openSite(site) {
      const response = await page.goto(fixture.control + "/admin/sites/account-create/" + site);
      check(response?.status() === 200, "selected site account form unavailable");
      const form = page.locator("form[data-control-account-create]");
      check(await form.count() === 1 && await form.locator("input[name=site_id]").inputValue() === site &&
        await form.locator("input[name=site_id]").getAttribute("type") === "hidden",
        "form site scope is not exact");
      const scope = await page.locator("main").textContent();
      check(scope.includes(site) && scope.includes(fixture[site === "site-a" ? "site_a" : "site_b"]),
        "selected site identity/origin not visible");
      check(await form.locator('input[name=password]').getAttribute("autocomplete") === "new-password",
        "password autocomplete contract absent");
      await page.evaluate(() => {
        window.__accountEvents = [];
        const form = document.querySelector("form[data-control-account-create]");
        form.addEventListener("admin:success", e => window.__accountEvents.push({type:"success", id:e.detail.id}));
        form.addEventListener("admin:failure", e => window.__accountEvents.push({
          type:"failure", status:e.detail.status, unknown:e.detail.unknown, code:e.detail.server?.code
        }));
      });
      return form;
    }
    async function submit(form, login, password) {
      await form.locator("input[name=login]").fill(login);
      await form.locator("input[name=password]").fill(password);
      await form.locator("button[type=submit]").click();
      await page.waitForFunction(() => window.__accountEvents?.length > 0);
      const event = await page.evaluate(() => window.__accountEvents.shift());
      await page.waitForFunction(() => document.querySelector("form[data-control-account-create]")?.getAttribute("aria-busy") === "false");
      check(await form.locator("input[name=password]").inputValue() === "", "password remained in DOM");
      return event;
    }
    let form = await openSite("site-a");
    for (const width of [390,1366]) {
      await page.setViewportSize({width,height:900});
      const bounds = await page.evaluate(() => [document.documentElement.scrollWidth, innerWidth]);
      check(bounds[0] <= bounds[1] + 2, "form has horizontal overflow at " + width);
      const field = form.locator("input[name=login]");
      await field.focus();
      check(await field.evaluate(e => document.activeElement === e), "keyboard focus failed");
      await page.keyboard.press("Tab");
      check(await page.evaluate(() => document.activeElement !== document.body), "Tab lost focus");
    }
    record("en-ltr-keyboard-responsive");

    const success = await submit(form, "browser-account-a", "Browser-A-Password-2026!");
    check(success.type === "success" && typeof success.id === "string" && success.id.length > 0,
      "site-A account creation did not acknowledge its account");
    await notice("completed", "status");
    record("site-a-create-acknowledged");

    const duplicate = await submit(form, "browser-account-a", "Browser-Duplicate-2026!");
    check(duplicate.type === "failure" && duplicate.status === 409 && duplicate.unknown === false &&
      duplicate.code === "conflict", "duplicate login was not deterministically rejected");
    await notice("record_conflict", "alert");
    record("duplicate-conflict-no-unknown-replay");

    const languagePage = await page.goto(fixture.control + "/admin/language/");
    check(languagePage?.status() === 200, "language selection page unavailable");
    await page.locator("select[name=ui_language]").selectOption("fa");
    await Promise.all([
      page.waitForNavigation({waitUntil:"domcontentloaded"}),
      page.locator("form:has(select[name=ui_language]) button[type=submit]").click()
    ]);
    check(await page.locator("html").getAttribute("lang") === "fa" &&
      await page.locator("html").getAttribute("dir") === "rtl", "Persian RTL language unavailable");
    form = await openSite("site-b");
    check(await page.locator("html").getAttribute("lang") === "fa" &&
      await page.locator("html").getAttribute("dir") === "rtl", "selected form ignored locale");
    record("fa-rtl-selected-site");

    // Suppress ONE response only after the real site-B identity write commits.
    // Never automatically repeat the write: later check original user credentials.
    let intercepted = 0, committedSiteBID = "";
    const routeErrors = [];
    await context.route("**/admin/sites/account-create", async route => {
      try {
      const req = route.request();
      const address = new URL(req.url());
      check(req.method() === "POST" && address.origin === fixture.control &&
        address.pathname === "/admin/sites/account-create", "unexpected interception target");
      intercepted++;
      check(intercepted === 1, "uncertain write was replayed");
      const body = req.postData();
      const parsed = JSON.parse(body);
      check(parsed.site_id === "site-b" && parsed.login === "browser-account-b" &&
        typeof parsed.password === "string" && body.length <= 4096, "wrong test mutation");
      const headers = await req.allHeaders();
      check(headers.origin === fixture.control && headers.cookie && headers["x-csrf-token"],
        "missing trusted origin/session/CSRF");
      // Node's DNS does not always resolve *.localhost. Dial only the fixture's
      // 127.0.0.1 listener, while preserving Host, SNI, Origin and raw request.
      const committed = await new Promise((resolve,reject) => {
        const call = https.request({
          hostname:"127.0.0.1", port:Number(address.port), servername:"control.localhost",
          rejectUnauthorized:false, method:"POST", path:address.pathname,
          headers:{
            host:address.host, origin:fixture.control,
            cookie:headers.cookie, "x-identity-request":headers["x-identity-request"],
            "x-csrf-token":headers["x-csrf-token"], "content-type":"application/json",
            "content-length":Buffer.byteLength(body)
          }
        }, res => {
          let bytes = "";
          res.on("data", chunk => { bytes += chunk; if(bytes.length>4096) call.destroy(new Error("response too large")); });
          res.on("end", () => resolve({status:res.statusCode,body:bytes}));
          res.on("error", reject);
        });
        call.setTimeout(12000, () => call.destroy(new Error("fixture request timeout")));
        call.on("error", reject);
        call.end(body);
      });
      const committedResult = committed.status === 200 ? JSON.parse(committed.body) : {};
      check(committed.status === 200 && /^[A-Z2-7]{26}$/.test(committedResult.id),
        "real site-B write failed before acknowledgement suppression");
      committedSiteBID = committedResult.id;
      await route.abort("failed");
      } catch (error) {
        routeErrors.push(error.message);
        await route.abort("failed").catch(() => {});
      }
    });
    const uncertain = await submit(form, "browser-account-b", "Browser-B-Password-2026!");
    // Keep fault route armed: a delayed replay cannot hide behind HTTP 409.
    await page.waitForTimeout(650);
    assertMutations();
    check(routeErrors.length === 0 && intercepted === 1 &&
      uncertain.type === "failure" && uncertain.unknown === true,
      "committed lost-ack response did not remain uncertain or was replayed");
    await notice("outcome_unknown", "alert");
    record("site-b-committed-lost-ack-no-replay");

    // Use the normal Identity endpoint via a fresh same-origin browser context,
    // never a caller-supplied principal or Control privileged credential.
    const remote = await browser.newContext({ignoreHTTPSErrors: true});
    try {
      const verify = async (origin, login, password) => {
        const p = await remote.newPage();
        await p.goto(origin + "/admin/login");
        const result = await p.evaluate(async ({login,password}) => {
          const response = await fetch("/auth/login", {
            method:"POST", credentials:"same-origin",
            headers:{"Content-Type":"application/json","X-Identity-Request":"1"},
            body:JSON.stringify({login,password})
          });
          let code = "";
          try { code = (await response.json()).code || ""; } catch (_) {}
          return {status:response.status,code};
        }, {login,password});
        await p.close();
        return result;
      };
      check((await verify(fixture.site_a,"browser-account-a","Browser-A-Password-2026!")).status === 200,
        "actual site-A Identity cannot authenticate its new account");
      check((await verify(fixture.site_b,"browser-account-b","Browser-B-Password-2026!")).status === 200,
        "actual site-B Identity cannot reconcile committed account");
      const wrongSiteA = await verify(fixture.site_a,"browser-account-b","Browser-B-Password-2026!");
      const wrongSiteB = await verify(fixture.site_b,"browser-account-a","Browser-A-Password-2026!");
      check(wrongSiteA.status === 401 && wrongSiteA.code === "authentication_failed" &&
        wrongSiteB.status === 401 && wrongSiteB.code === "authentication_failed",
        "cross-site Identity denial was not HTTP 401 authentication_failed");
    } finally { await remote.close(); }
    record("identity-reconciliation-and-cross-site-denial");

    for (const selector of ["disabled","unknown","site-a/forged"]) {
      const response = await page.goto(fixture.control + "/admin/sites/account-create/" + selector);
      check(response?.status() === 403 &&
        await page.locator("form[data-control-account-create]").count() === 0,
        "unauthorized selector did not return HTTP 403 without form");
    }
    record("disabled-unknown-forged-site-denials");

    // Bounded observation persists through subsequent navigation.
    await page.waitForTimeout(650);
    assertMutations();
    check(routeErrors.length === 0 && intercepted === 1 && committedSiteBID.length === 26,
      "fault injection intercepted multiple attempts or missing real commit");
    check(errors.length === 0, "unhandled browser errors: " + errors.join("; "));
    check(await page.evaluate(() => localStorage.length === 0 && sessionStorage.length === 0),
      "account workflow put secrets in browser storage");
    record("no-browser-errors-no-webstorage");
    const result = {schema:fixture.schema,binary_sha256:fixture.binary_sha256,checks,
      account_ids:{"site-a":success.id, "site-b":committedSiteBID},
      mutation_attempts:Object.fromEntries(["browser-account-a","browser-account-b"].map(login =>
        [login,accountMutations.filter(m => m.login === login).length])),
      injected_commits:intercepted};
    const file = await fs.open(path.join(root, "result.json"), "wx", 0o600);
    try {
      await file.writeFile(JSON.stringify(result)+"\n");
      await file.sync();
    } finally { await file.close(); }
    console.log(JSON.stringify({schema:result.schema,binary_sha256:result.binary_sha256,checks}));
  } finally {
    if (context) await context.close();
    await browser.close();
  }
}
main().catch(error => { console.error("Control account Chromium acceptance FAILED:", error.message); process.exitCode=1; });
