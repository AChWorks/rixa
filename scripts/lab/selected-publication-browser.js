// SPDX-License-Identifier: MPL-2.0
"use strict";

// Browser-driven, real-control-login, real-PostgreSQL selected-site publication.
// This code is intended only for the explicitly disposable loopback fixture.
async function rixaSelectedPublicationBrowser({browser, context, page, fixture, privateRoot}) {
  const check = (ok, message) => { if (!ok) throw new Error(message); };
  check(fixture && fixture.schema === "rixa.selected-site-publication-browser.v1", "wrong publication fixture");
  for (const [name, host] of [["control", "control.localhost"], ["site_a", "a.localhost"], ["site_b", "b.localhost"]]) {
    const origin = new URL(fixture[name]);
    check(origin.protocol === "https:" && origin.hostname === host && origin.port &&
      origin.origin === fixture[name], "only the three explicit loopback origins are allowed");
  }
  check(fixture.sites.length === 2 && fixture.sites[0].id === "site-a" &&
    fixture.sites[1].id === "site-b", "two distinct sites are required");
  const fs = await import("node:fs/promises");
  const https = await import("node:https");
  const stat = await fs.lstat(privateRoot);
  check(stat.isDirectory() && !stat.isSymbolicLink() && (stat.mode & 0o077) === 0 &&
    await fs.realpath(privateRoot) === privateRoot, "private 0700 fixture root required");
  const journal = await fs.open(privateRoot + "/publication-attempts.jsonl", "wx", 0o600);
  let writes = Promise.resolve();
  const record = data => writes = writes.then(async () => {
    await journal.write(JSON.stringify({at: new Date().toISOString(), ...data}) + "\n");
    await journal.sync();
  });
  const checks = [];
  const mark = value => checks.push(value);
  const errors = [];
  page.setDefaultTimeout(15000);
  page.on("pageerror", error => errors.push(error.message));

  async function localRequest(rawURL, method = "GET", headers = {}, body) {
    const target = new URL(rawURL);
    check([fixture.control, fixture.site_a, fixture.site_b].includes(target.origin), "unrecognized origin");
    const sendHeaders = {...headers, host: target.host};
    if (body !== undefined) sendHeaders["content-length"] = Buffer.byteLength(body);
    return new Promise((resolve, reject) => {
      const req = https.request({
        hostname: "127.0.0.1", port: Number(target.port), servername: target.hostname,
        rejectUnauthorized: false, method, path: target.pathname + target.search, headers: sendHeaders
      }, response => {
        const chunks = [];
        let bytes = 0;
        response.on("data", chunk => {
          bytes += chunk.length;
          if (bytes > 2 * 1024 * 1024) return req.destroy(new Error("response too large"));
          chunks.push(chunk);
        });
        response.on("error", reject);
        response.on("end", () => resolve({
          status: response.statusCode, text: Buffer.concat(chunks).toString("utf8")
        }));
      });
      req.setTimeout(10000, () => req.destroy(new Error("loopback HTTP timeout")));
      req.on("error", reject);
      if (body !== undefined) req.write(body);
      req.end();
    });
  }

  const site = fixture.sites[0];
  const base = "/admin/sites/site/site-a/content";
  const route = "/posts/control-browser-proof/";
  const pattern = /\/admin\/sites\/site\/site-a\/content\/(publication-intent|publication-intent-clear|publish)$/;
  let failNextPublishAcknowledgement = false;
  let lostReceipt;
  let resolveLost;
  const lostForwarded = new Promise(resolve => { resolveLost = resolve; });
  await context.route(pattern, async intercepted => {
    const request = intercepted.request();
    if (request.method() !== "POST") return intercepted.continue();
    try {
      check(new URL(request.url()).origin === fixture.control, "wrong authenticated origin");
      const payload = request.postData();
      check(payload && payload.length < 16 * 1024, "oversized mutation");
      const data = JSON.parse(payload);
      check(typeof data.operation_id === "string" && typeof data.id === "string",
        "immutable operation ID or content ID missing");
      await record({type: "mutation", site_id: "site-a",
        operation_id: data.operation_id, path: new URL(request.url()).pathname, payload});
      if (failNextPublishAcknowledgement && request.url().endsWith("/publish")) {
        failNextPublishAcknowledgement = false;
        const result = await localRequest(request.url(), "POST", await request.allHeaders(), payload);
        await record({type: "effect_receipt_before_lost_ack", site_id: "site-a",
          operation_id: data.operation_id, status: result.status});
        check(result.status === 200, "withdrawal was not committed before simulated lost acknowledgement");
        lostReceipt = JSON.parse(result.text);
        resolveLost();
        return intercepted.abort("failed");
      }
      return intercepted.continue();
    } catch (error) {
      errors.push("interception: " + String(error.message));
      await intercepted.abort("failed").catch(() => {});
      resolveLost();
    }
  });

  const result = {binary_sha256: fixture.binary_sha256, checks};
  try {
    await page.setViewportSize({width: 1366, height: 900});
    let response = await page.goto(fixture.control + "/admin/login");
    check(response.status() === 200, "real control login page failed");
    await page.locator("form[data-login] input[name=login]").fill("control-admin");
    await page.locator("form[data-login] input[name=password]").fill("A-Strong-Control-Password-2026!");
    await Promise.all([
      page.waitForURL(url => url.origin === fixture.control && url.pathname !== "/admin/login"),
      page.locator("form[data-login] button[type=submit]").click()
    ]);
    await page.locator("#logout").waitFor();
    check((await context.cookies(fixture.site_a)).length === 0, "control login leaked a site cookie");
    mark("real-control-login-and-cookie-isolation");

    const bBefore = await localRequest(fixture.site_b + "/retained/");
    check(bBefore.status === 200 && bBefore.text.includes("site-b"), "site B baseline missing");
    const absentB = await localRequest(fixture.site_b + route);
    check(absentB.status === 404, "site B unexpectedly owns selected route");
    response = await page.goto(fixture.control + base + "/edit/" + site.content_id);
    check(response.status() === 200, "selected control editor inaccessible");
    check(await page.locator("[data-content-workspace]").getAttribute("data-site-id") === "site-a", "wrong site binding");
    check(await page.locator("[data-content-workspace]").getAttribute("data-origin") === fixture.site_a, "wrong origin binding");
    check(await page.locator("[data-current-revision]").textContent() === site.head, "unexpected seeded saved head");
    check(await page.locator("html").getAttribute("lang") === "en" &&
      await page.locator("html").getAttribute("dir") === "ltr", "English/LTR missing");
    check(await page.locator("[data-publication-apply]").count() === 1, "publication controls unavailable");
    await page.setViewportSize({width: 390, height: 844});
    check(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 2), "390px publication editor overflow");
    await page.locator("[data-publication-form] input[name=revision]").focus();
    check(await page.evaluate(() => document.activeElement?.getAttribute("name") === "revision"), "keyboard focus unavailable");
    await page.keyboard.press("Tab");
    await page.screenshot({path: privateRoot + "/publication-en-390.png", fullPage: true});
    mark("en-ltr-390-keyboard");

    // Selecting a revision must NOT immediately alter the public site.
    await page.locator("[data-publication-form] input[name=revision]").fill(site.head);
    await page.locator("[data-publication-form] button[type=submit]").click();
    await page.waitForFunction(head => document.querySelector("[data-publication-intent-output]")?.textContent === head, site.head);
    check((await localRequest(fixture.site_a + route)).status === 404, "intent alone published the route");
    mark("reviewed-intent-is-not-publication");

    const apply = page.locator("[data-publication-apply]");
    const priorGeneration = await apply.locator("[data-publication-generation]").inputValue();
    await apply.locator("input[name=route]").fill(route);
    await apply.locator("[data-confirm]").check();
    // Any route change invalidates an already checked explicit approval.
    await apply.locator("input[name=route]").fill(route + "changed/");
    check(!(await apply.locator("[data-confirm]").isChecked()), "route change retained stale confirmation");
    await apply.locator("input[name=route]").fill(route);
    await apply.locator("[data-confirm]").check();
    await apply.locator("button[type=submit]").click();
    await page.waitForFunction(previous => {
      const gen = document.querySelector("[data-publication-generation-output]")?.textContent || "";
      return /^[A-Z2-7]{26}$/.test(gen) && gen !== previous &&
        !document.querySelector("[data-publication-result]")?.hidden;
    }, priorGeneration);
    const publishedGeneration = await page.locator("[data-publication-generation-output]").textContent();
    const aPublished = await localRequest(fixture.site_a + route);
    check(aPublished.status === 200 && aPublished.text.includes("Private selected revision 56"), "real public HTTPS revision not served");
    check((await localRequest(fixture.site_b + route)).status === 404, "site A publication crossed to B");
    mark("confirmed-site-wide-apply-visible-on-real-https");

    // A changed language is an interface preference, not new site authority.
    await page.goto(fixture.control + "/admin/language/");
    await page.locator("select[name=ui_language]").selectOption("fa");
    await Promise.all([
      page.waitForNavigation({waitUntil: "domcontentloaded"}),
      page.locator("form:has(select[name=ui_language]) button[type=submit]").click()
    ]);
    response = await page.goto(fixture.control + base + "/edit/" + site.content_id);
    check(response.status() === 200 && await page.locator("html").getAttribute("lang") === "fa" &&
      await page.locator("html").getAttribute("dir") === "rtl", "Persian/RTL control origin missing");
    await page.setViewportSize({width: 1366, height: 900});
    await page.screenshot({path: privateRoot + "/publication-fa-wide.png", fullPage: true});
    mark("fa-rtl-wide");

    await page.locator("[data-publication-clear] button[type=submit]").click();
    await page.waitForFunction(() => document.querySelector("[data-publication-clear]")?.hidden === true);
    check((await localRequest(fixture.site_a + route)).status === 200, "clearing intent auto-withdrew public output");
    mark("clear-intent-does-not-auto-withdraw");

    // Simulate lost response *after* the actual selected-site withdrawal committed.
    const withdraw = page.locator("[data-publication-apply]");
    const withdrawID = await withdraw.locator("input[name=operation_id]").inputValue();
    const withdrawGeneration = await withdraw.locator("[data-publication-generation]").inputValue();
    check(withdrawGeneration === publishedGeneration, "withdrawal did not review current generation");
    check(await withdraw.locator("input[name=expected_intent_revision]").inputValue() === "0",
      "cleared intent not carried to Apply");
    await withdraw.locator("[data-confirm]").check();
    failNextPublishAcknowledgement = true;
    await withdraw.locator("button[type=submit]").click();
    await lostForwarded;
    check(lostReceipt && /^[A-Z2-7]{26}$/.test(lostReceipt.generation) &&
      lostReceipt.generation !== publishedGeneration && lostReceipt.operation_id === withdrawID,
      "lost acknowledgement was not from actual committed withdrawal");
    const aGone = await localRequest(fixture.site_a + route);
    check(aGone.status === 410, "withdrawn real public HTTPS route is not 410 Gone");
    await page.locator("[data-operation-lookup-input]").fill(withdrawID);
    await page.locator("[data-operation-lookup] button[type=submit]").click();
    await page.waitForFunction(gen => document.getElementById("status")?.textContent?.includes(gen), lostReceipt.generation);
    check((await page.locator("#status").textContent()).includes(withdrawID),
      "same-operation inspection omitted the committed identity");
    mark("lost-ack-actual-commit-inspect-same-operation");

    const bAfter = await localRequest(fixture.site_b + "/retained/");
    check(bAfter.status === 200 && bAfter.text === bBefore.text &&
      (await localRequest(fixture.site_b + route)).status === 404,
      "site B public generation changed");
    check(errors.length === 0, "product/browser JavaScript errors: " + errors.join("; "));
    mark("site-b-preserved-and-no-js-errors");
    await record({type: "complete", site_id: "site-a", checks: [...checks],
      published_generation: publishedGeneration, withdrawn_generation: lostReceipt.generation});
    return result;
  } finally {
    await writes;
    await journal.close();
  }
}
