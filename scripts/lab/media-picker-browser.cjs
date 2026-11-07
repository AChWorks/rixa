// SPDX-License-Identifier: MPL-2.0
"use strict";

// Real Chromium accepts the site-origin AChrix Media Picker with two real
// PostgreSQL-backed sites, verifying exact operations and private-file integrity.
const fs = require("node:fs/promises");
const path = require("node:path");
const {chromium} = require("playwright");

const check = (ok, description) => { if (!ok) throw new Error(description); };
async function main() {
  const root = process.argv[2];
  check(root && path.isAbsolute(root) && root !== "/" && path.normalize(root) === root,
    "new absolute private fixture root required");
  const info = await fs.lstat(root);
  check(info.isDirectory() && !info.isSymbolicLink() && (info.mode & 0o077) === 0 &&
    await fs.realpath(root) === root, "refusing nonprivate or aliased fixture root");
  const fixture = JSON.parse(await fs.readFile(path.join(root, "ready.json"), "utf8"));
  check(fixture.schema === "rixa.media-picker-browser-fixture.v1" &&
    /^[a-f0-9]{64}$/.test(fixture.binary_sha256) &&
    fixture.sites.length === 2, "unexpected browser fixture");
  for (const [i, host] of ["a.localhost", "b.localhost"].entries()) {
    const site = fixture.sites[i], url = new URL(site.origin);
    check(site.id === "site-" + (i ? "b" : "a") &&
      url.protocol === "https:" && url.hostname === host &&
      site.origin === url.origin && /^[A-Z2-7]{26}$/.test(site.asset_id) &&
      /^[A-Z2-7]{26}$/.test(site.content_id), "foreign site/asset in fixture");
  }

  const checks = [];
  const sites = {};
  const errors = [];
  const browser = await chromium.launch({headless:true});
  try {
    for (const [index, site] of fixture.sites.entries()) {
      const context = await browser.newContext({
        ignoreHTTPSErrors:true, viewport:{width:index ? 1366 : 390,height:900}
      });
      try {
        const page = await context.newPage();
        page.setDefaultTimeout(15000);
        page.on("pageerror", e => errors.push(e.message));
        const origin = site.origin;
        const loginResponse = await page.goto(origin + "/admin/login");
        check(loginResponse.status() === 200,"site login unavailable");
        await page.locator("form[data-login] input[name=login]").fill(index ? "site-b-admin" : "site-a-admin");
        await page.locator("form[data-login] input[name=password]").fill(index ? "A-Strong-Site-B-Password-2026!" : "A-Strong-Site-A-Password-2026!");
        await Promise.all([
          page.waitForURL(u => u.origin === origin && u.pathname !== "/admin/login"),
          page.locator("form[data-login] button[type=submit]").click()
        ]);
        await page.locator("#logout").waitFor();
        if (index === 0) {
          await page.goto(origin + "/admin/language/");
          await page.locator("select[name=ui_language]").selectOption("fa");
          await Promise.all([
            page.waitForNavigation({waitUntil:"domcontentloaded"}),
            page.locator("form:has(select[name=ui_language]) button[type=submit]").click()
          ]);
        }
        const response = await page.goto(origin + "/admin/content/edit/" + site.content_id);
        check(response.status() === 200, "site editor unavailable");
        check(await page.locator("html").getAttribute("lang") === (index ? "en" : "fa") &&
          await page.locator("html").getAttribute("dir") === (index ? "ltr" : "rtl"),
          "site editor locale/RTL mismatch");
        check(await page.locator('script[src="/admin/media/picker.js"]').count() === 1 &&
          await page.locator("achrix-media-picker").count() === 1 &&
          await page.locator('achrix-media-picker[accept="image/png,image/jpeg"]').count() === 1,
          "released Media Picker is missing from site editor");

        // No "selected site" implicit scope or Control session is involved.
        const api = await page.evaluate(async () => {
          const response = await fetch("/admin/media/picker/page", {
            credentials:"same-origin",cache:"no-store",headers:{Accept:"application/json"}
          });
          return {status:response.status,data:await response.json()};
        });
        check(api.status === 200 && Array.isArray(api.data.assets) &&
          api.data.assets.some(a => a.id === site.asset_id) &&
          !api.data.assets.some(a => a.id === fixture.sites[1-index].asset_id),
          "Picker leaked foreign site or did not list own ready asset");
        const foreign = await page.evaluate(async foreignID => {
          const response = await fetch("/admin/media/picker/asset/" + foreignID, {
            credentials:"same-origin",cache:"no-store",headers:{Accept:"application/json"}
          });
          return {status:response.status,body:await response.text()};
        }, fixture.sites[1-index].asset_id);
        check(foreign.status === 404 && !foreign.body.includes(fixture.sites[1-index].asset_id),
          "exact foreign asset lookup exposed content");

        const picker = page.locator("achrix-media-picker");
        await picker.locator("summary").click();
        await picker.locator("ul li button").first().waitFor();
        let writes = 0;
        page.on("request", request => {
          if (request.method() === "POST" && request.url().includes("/admin/content/save")) writes++;
        });
        const figureCount = await page.locator("[data-editor] figure").count();
        // A synthetic large exact string verifies that the product listener
        // never passes a Media revision through JavaScript Number precision.
        await page.evaluate(id => {
          document.querySelector("achrix-media-picker").dispatchEvent(
            new CustomEvent("achrix:media-selected", {
              bubbles:true,detail:{id,revision:"9007199254740993",mime:"image/png"}
            })
          );
        },site.asset_id);
        check(await page.locator("[data-media-id]").inputValue() === site.asset_id &&
          await page.locator("[data-media-revision]").textContent() === "9007199254740993" &&
          writes === 0 && await page.locator("[data-editor] figure").count() === figureCount,
          "synthetic selection caused an edit or lost exact revision");

        await picker.locator("ul li button").first().click();
        await page.waitForFunction(id => {
          const field = document.querySelector("[data-media-id]");
          const output = document.querySelector("[data-media-revision]");
          return field?.value === id && output && /^[1-9][0-9]{0,18}$/.test(output.textContent) &&
            output.textContent !== "9007199254740993";
        },site.asset_id);
        check(writes === 0 && await page.locator("[data-editor] figure").count() === figureCount,
          "real selection silently saved/published content");
        await page.locator("[data-media-caption]").fill("Picker فارسی " + site.id);
        await page.locator("[data-add-media]").click();
        check(await page.locator("[data-editor] figure").count() === figureCount + 1 &&
          await page.locator('[data-editor] figure[data-media-id="' + site.asset_id + '"]').count() >= 1,
          "selected asset did not insert as a proposed figure");

        const previous = await page.locator("[data-current-revision]").textContent();
        const operationID = await page.locator("[data-content-save] input[name=operation_id]").inputValue();
        check(/^[A-Z2-7]{26}$/.test(operationID), "missing bounded real Save operation identity");
        await page.locator("[data-content-save] button[type=submit]").click();
        await page.waitForFunction(before => {
          const value = document.querySelector("[data-current-revision]")?.textContent;
          return value && value !== before;
        },previous);
        check(writes === 1, "selection changed draft without exactly one explicit Save");
        const newHead = await page.locator("[data-current-revision]").textContent();
        const preview = await context.newPage();
        try {
          const answer = await preview.goto(origin + "/admin/content/preview/" + site.content_id + "/" + newHead);
          check(answer.status() === 200 && await preview.locator("img").count() === 0,
            "private original inlined in draft preview");
        } finally {await preview.close();}
        sites[site.id] = {operation_id:operationID,content_id:site.content_id,
          asset_id:site.asset_id,revision:newHead};
        checks.push(site.id + "-picker-ready-site-isolation-fa-en-explicit-save-private-preview");
      } finally {await context.close();}
    }

    // Control-origin acceptance uses the same real HTTPS/PG fixture, but only
    // the Control session. Two tabs have explicit, independent site endpoints.
    const controlSites = {}, controlChecks = [];
    const controlOrigin = fixture.control;
    const controlURL = new URL(controlOrigin);
    check(controlURL.protocol === "https:" && controlURL.hostname === "control.localhost" &&
      controlURL.pathname === "/" && !controlURL.search, "invalid fixture Control origin");
    const control = await browser.newContext({ignoreHTTPSErrors:true, viewport:{width:1366,height:900}});
    try {
      const tabA = await control.newPage(), tabB = await control.newPage();
      for (const tab of [tabA,tabB]) {
        tab.setDefaultTimeout(15000);
        tab.on("pageerror", e => errors.push(e.message));
      }
      const login = await tabA.goto(controlOrigin + "/admin/login");
      check(login.status() === 200, "Control login unavailable");
      await tabA.locator("form[data-login] input[name=login]").fill("control-admin");
      await tabA.locator("form[data-login] input[name=password]").fill("A-Strong-Control-Password-2026!");
      await Promise.all([
        tabA.waitForURL(u => u.origin === controlOrigin && u.pathname !== "/admin/login"),
        tabA.locator("form[data-login] button[type=submit]").click()
      ]);
      await tabA.locator("#logout").waitFor();
      check((await control.cookies(controlOrigin)).some(c => c.name === "__Host-AChrix-Session") &&
        (await control.cookies(fixture.sites[0].origin)).length === 0 &&
        (await control.cookies(fixture.sites[1].origin)).length === 0,
        "Control media browsing borrowed Site Admin credentials");

      const tabs = [tabA,tabB], apiRequests = [[],[]], mutations = [[],[]];
      for (const [index, site] of fixture.sites.entries()) {
        const tab = tabs[index], root = "/admin/sites/site/" + site.id + "/media/picker";
        tab.on("request", req => {
          const target = new URL(req.url());
          if (target.pathname.includes("/media/picker/")) apiRequests[index].push({origin:target.origin,path:target.pathname});
          if (req.method() === "POST" && target.pathname.endsWith("/content/save")) {
            mutations[index].push({origin:target.origin,path:target.pathname});
          }
        });
        const r = await tab.goto(controlOrigin + "/admin/sites/site/" + site.id +
          "/content/edit/" + site.content_id);
        check(r.status() === 200, "Control selected editor unavailable: " + site.id);
        check(await tab.locator("html").getAttribute("lang") === "en" &&
          await tab.locator("[data-content-workspace]").getAttribute("data-site-id") === site.id &&
          await tab.locator("[data-content-workspace]").getAttribute("data-origin") === site.origin,
          "Control view has wrong site/locale");
        check(await tab.locator('script[src="/admin/sites/picker.js"]').count() === 1 &&
          await tab.locator("achrix-media-picker").getAttribute("endpoint") === root &&
          await tab.locator('script[src="/admin/media/picker.js"]').count() === 0,
          "Control Picker does not use official same-origin per-site endpoint");
        await tab.locator("achrix-media-picker summary").waitFor();
        const result = await tab.evaluate(async route => {
          const res = await fetch(route, {credentials:"same-origin",cache:"no-store",headers:{Accept:"application/json"}});
          return {status:res.status,body:await res.json()};
        },root + "/page");
        check(result.status === 200 && Array.isArray(result.body.assets) &&
          result.body.assets.some(a => a.id === site.asset_id) &&
          !result.body.assets.some(a => a.id === fixture.sites[1-index].asset_id) &&
          result.body.assets.every(a => a.state === "ready" && typeof a.revision === "string" &&
            !Object.hasOwn(a,"sha256") && !Object.hasOwn(a,"storage_root") && !Object.hasOwn(a,"bytes")),
          "Control BFF exposed foreign/unready or private asset metadata");
        const foreign = await tab.evaluate(async ([route,id]) => {
          const res = await fetch(route + "/asset/" + id,{credentials:"same-origin",cache:"no-store"});
          return {status:res.status,body:await res.text()};
        },[root,fixture.sites[1-index].asset_id]);
        check(foreign.status === 404 && !foreign.body.includes(fixture.sites[1-index].asset_id),
          "Control exact foreign asset metadata escaped site boundary");
        const forbidden = await tab.evaluate(async () => {
          const res = await fetch("/admin/sites/site/disabled/media/picker/page",{credentials:"same-origin",cache:"no-store"});
          return {status:res.status,body:await res.text()};
        });
        check(forbidden.status === 403 && !forbidden.body.includes(site.asset_id),
          "disabled Control site exposed Media");
      }

      // Exercise the official released component's detached-control fencing
      // in the actual Control origin, not only the AChrix synthetic fixture.
      await tabA.locator("achrix-media-picker summary").click();
      await tabA.locator("achrix-media-picker ul li button").first().waitFor();
      await tabA.waitForFunction(() => document.querySelector("achrix-media-picker")?.busy === false);
      await tabA.evaluate(() => {
        const picker = document.querySelector("achrix-media-picker");
        window.__oldPickerControls = {
          refresh:picker.refresh,next:picker.next,asset:picker.list.querySelector("button")
        };
        window.__pickerProposals = [];
        picker.addEventListener("achrix:media-selected", event => window.__pickerProposals.push(event.detail.id));
        picker.setAttribute("endpoint", "/admin/sites/site/site-b/media/picker");
      });
      await tabA.locator("achrix-media-picker summary").click();
      await tabA.locator("achrix-media-picker ul li button").first().waitFor();
      await tabA.waitForFunction(() => document.querySelector("achrix-media-picker")?.busy === false);
      const beforeStale = apiRequests[0].length;
      const stale = await tabA.evaluate(() => {
        const old = window.__oldPickerControls;
        const disconnected = !old.refresh.isConnected && !old.next.isConnected && !old.asset.isConnected;
        old.refresh.click(); old.next.click(); old.asset.click();
        return {disconnected,proposals:window.__pickerProposals.length};
      });
      await tabA.waitForTimeout(100);
      check(stale.disconnected && stale.proposals === 0 && apiRequests[0].length === beforeStale &&
        await tabA.locator("[data-media-id]").inputValue() === "",
        "detached site-A controls affected newly selected site-B Picker");
      await tabA.evaluate(() => document.querySelector("achrix-media-picker")
        .setAttribute("endpoint", "/admin/sites/site/site-a/media/picker"));
      await tabA.locator("achrix-media-picker summary").click();
      await tabA.locator("achrix-media-picker ul li button").first().waitFor();
      await tabA.waitForFunction(() => document.querySelector("achrix-media-picker")?.busy === false);
      const beforeReturn = apiRequests[0].length;
      await tabA.evaluate(() => {
        const old = window.__oldPickerControls;
        old.refresh.click(); old.next.click(); old.asset.click();
      });
      await tabA.waitForTimeout(100);
      check(apiRequests[0].length === beforeReturn &&
        await tabA.locator("[data-media-id]").inputValue() === "",
        "A->B->A accepted a stale original DOM action");

      // Only an explicit user action proposes a Media reference, only Save
      // writes, and each tab must retain its own exact bound site and operator.
      for (const [index, site] of fixture.sites.entries()) {
        const tab = tabs[index], picker = tab.locator("achrix-media-picker");
        if (index === 1) {
          await picker.locator("summary").click();
          await picker.locator("ul li button").first().waitFor();
        }
        const startFigures = await tab.locator("[data-editor] figure").count();
        await picker.locator("ul li button").first().click();
        await tab.waitForFunction(id => document.querySelector("[data-media-id]")?.value === id,site.asset_id);
        check(/^[1-9][0-9]{0,18}$/.test(await tab.locator("[data-media-revision]").textContent()) &&
          mutations[index].length === 0 &&
          await tab.locator("[data-editor] figure").count() === startFigures,
          "Control selection saved content or lost exact Media revision");
        // Invalid MIME proposals never replace the trusted selected asset.
        await tab.evaluate(foreign => document.querySelector("achrix-media-picker").dispatchEvent(
          new CustomEvent("achrix:media-selected",{detail:{
            id:foreign,revision:"9007199254740993",mime:"text/html"
          }})
        ),fixture.sites[1-index].asset_id);
        check(await tab.locator("[data-media-id]").inputValue() === site.asset_id,
          "unsupported Media MIME proposal replaced Control selection");
        await tab.locator("[data-media-caption]").fill("Control فارسی " + site.id);
        await tab.locator("[data-add-media]").click();
        check(await tab.locator("[data-editor] figure").count() === startFigures + 1,
          "Control selection was not inserted as a pending figure");
      }
      const priorHeads = await Promise.all(tabs.map(t => t.locator("[data-current-revision]").textContent()));
      const ops = await Promise.all(tabs.map(t => t.locator("[data-content-save] input[name=operation_id]").inputValue()));
      check(ops.every(id=>/^[A-Z2-7]{26}$/.test(id)) && ops[0] !== ops[1],
        "Control tabs have invalid/reused Save operation IDs");
      await Promise.all(tabs.map(t => t.locator("[data-content-save] button[type=submit]").click()));
      await Promise.all(tabs.map((t,index) => t.waitForFunction(
        old => document.querySelector("[data-current-revision]")?.textContent !== old, priorHeads[index]
      )));
      for (const [index,site] of fixture.sites.entries()) {
        const revision = await tabs[index].locator("[data-current-revision]").textContent();
        check(mutations[index].length === 1 && mutations[index][0].origin === controlOrigin &&
          mutations[index][0].path === "/admin/sites/site/" + site.id + "/content/save" &&
          apiRequests[index].every(req => req.origin === controlOrigin && req.path.startsWith("/admin/sites/site/")) &&
          Number(revision) > Number(sites[site.id].revision),
          "concurrent Control tab Save crossed site boundaries or failed");
        const preview = await control.newPage();
        try {
          const response = await preview.goto(controlOrigin + "/admin/sites/site/" + site.id +
            "/content/preview/" + site.content_id + "/" + revision);
          check(response.status() === 200 && await preview.locator("img").count() === 0 &&
            (await preview.locator(".selected-site").textContent()).includes(site.id),
            "Control private preview inlined Media or changed site");
        } finally {await preview.close();}
        controlSites[site.id] = {operation_id:ops[index],content_id:site.content_id,
          asset_id:site.asset_id,revision};
        controlChecks.push(site.id + "-control-scoped-picker-save");
      }

      // A manual foreign-ID reference must fail again at the actual Save
      // boundary. No cross-site BFF result/metadata can authorize that save.
      const siteA = fixture.sites[0], foreignID = fixture.sites[1].asset_id;
      await tabA.locator("[data-media-id]").fill(foreignID);
      await tabA.locator("[data-add-media]").click();
      const afterValid = await tabA.locator("[data-current-revision]").textContent();
      const deniedOperation = await tabA.locator("[data-content-save] input[name=operation_id]").inputValue();
      const badSave = tabA.waitForResponse(response =>
        response.url().includes("/admin/sites/site/site-a/content/save") &&
        response.request().method() === "POST");
      await tabA.locator("[data-content-save] button[type=submit]").click();
      const rejection = await badSave;
      check(rejection.status() === 400 && await tabA.locator("[data-current-revision]").textContent() === afterValid &&
        deniedOperation !== ops[0], "Save-time foreign asset recheck did not fail closed");

      // The same Control session localizes both the product editor and the
      // official Media catalog after changing to Persian/RTL.
      await tabB.goto(controlOrigin + "/admin/language/");
      await tabB.locator("select[name=ui_language]").selectOption("fa");
      await Promise.all([
        tabB.waitForNavigation({waitUntil:"domcontentloaded"}),
        tabB.locator("form:has(select[name=ui_language]) button[type=submit]").click()
      ]);
      const rtl = await tabB.goto(controlOrigin + "/admin/sites/site/site-b/content/edit/" + fixture.sites[1].content_id);
      check(rtl.status() === 200 && await tabB.locator("html").getAttribute("lang") === "fa" &&
        await tabB.locator("html").getAttribute("dir") === "rtl",
        "Control selected-site editor did not switch to Persian/RTL");
      await tabB.locator("achrix-media-picker summary").click();
      await tabB.locator("achrix-media-picker ul li button").first().waitFor();
      check((await tabB.locator("achrix-media-picker summary").textContent()).length > 1,
        "Control Persian official Media catalog failed to initialize");

      const unknown = await browser.newContext({ignoreHTTPSErrors:true});
      try {
        const unauthorized = await unknown.newPage();
        await unauthorized.goto(controlOrigin + "/admin/sites/site/site-a/media/picker/page");
        check(new URL(unauthorized.url()).pathname === "/admin/login" &&
          !(await unauthorized.locator("body").textContent()).includes(siteA.asset_id),
          "unauthenticated Control request acquired Media metadata");
      } finally {await unknown.close();}
    } finally {await control.close();}
    check(errors.length === 0, "Chromium page errors: " + errors.join("; "));
    check(checks.length === 2 && Object.keys(sites).length === 2,
      "browser did not finish both scoped checks");
    const result = {schema:"rixa.media-picker-browser.v2",binary_sha256:fixture.binary_sha256,checks,sites,control_checks:controlChecks,control_sites:controlSites};
    await fs.writeFile(path.join(root, "media-picker-result.json"), JSON.stringify(result) + "\n", {
      encoding:"utf8",flag:"wx",mode:0o600
    });
    console.log("RIXA_MEDIA_PICKER_BROWSER_PASS " + checks.concat(controlChecks).join(","));
  } finally {await browser.close();}
}
main().catch(error => {console.error(error); process.exitCode = 1;});
