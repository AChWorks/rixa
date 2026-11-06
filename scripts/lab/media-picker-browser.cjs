// SPDX-License-Identifier: MPL-2.0
"use strict";

// Real Chromium accepts the site-origin AChrix Media Picker with two real
// PostgreSQL-backed sites, and reuses the selected-site source/CAS journey.
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
  check(fixture.schema === "rixa.selected-site-browser.v1" &&
    /^[a-f0-9]{64}$/.test(fixture.binary_sha256) &&
    fixture.sites.length === 2, "unexpected browser fixture");
  for (const [i, host] of ["a.localhost", "b.localhost"].entries()) {
    const site = fixture.sites[i], url = new URL(site.origin);
    check(site.id === "site-" + (i ? "b" : "a") &&
      url.protocol === "https:" && url.hostname === host &&
      site.origin === url.origin && /^[A-Z2-7]{26}$/.test(site.asset_id) &&
      /^[A-Z2-7]{26}$/.test(site.content_id), "foreign site/asset in fixture");
  }

  const driverPath = path.join(__dirname, "selected-site-browser.js");
  // CommonJS Function compilation preserves Node dynamic imports without
  // --experimental-vm-modules or modifying the established acceptance driver.
  const selected = {};
  new Function("exports", (await fs.readFile(driverPath, "utf8")) +
    "\nexports.run = rixaSelectedSiteBrowser;")(selected);
  check(typeof selected.run === "function",
    "cannot load existing selected-site acceptance driver");
  const checks = [];
  const errors = [];
  const browser = await chromium.launch({headless:true});
  try {
    const control = await browser.newContext({ignoreHTTPSErrors:true,viewport:{width:1366,height:900}});
    try {
      const page = await control.newPage();
      await selected.run({
        browser, context:control, page, fixture, privateRoot:root
      });
      checks.push("existing-control-two-site-drafts-and-recovery");
    } finally {await control.close();}

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
        checks.push(site.id + "-picker-ready-site-isolation-fa-en-explicit-save-private-preview");
      } finally {await context.close();}
    }
    check(errors.length === 0, "Chromium page errors: " + errors.join("; "));
    const result = {schema:"rixa.media-picker-browser.v1",binary_sha256:fixture.binary_sha256,checks};
    await fs.writeFile(path.join(root, "media-picker-result.json"), JSON.stringify(result) + "\n", {
      encoding:"utf8",flag:"wx",mode:0o600
    });
    check(checks.length === 3, "browser did not finish all scoped checks");
    console.log("RIXA_MEDIA_PICKER_BROWSER_PASS " + checks.join(","));
  } finally {await browser.close();}
}
main().catch(error => {console.error(error); process.exitCode = 1;});
