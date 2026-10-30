// SPDX-License-Identifier: MPL-2.0
"use strict";

// Called with existing isolated Playwright objects and a sanitized receipt.
// This driver never builds/replaces a binary or creates deployment resources.
async function rixaLocalizationBrowser({browser, context, page, fixture, phase, screenshotRoot}) {
  const check = (value, message) => { if (!value) throw new Error(message); };
  check(fixture?.schema === "rixa.localization-browser.v1", "unexpected fixture receipt");
  check(phase === "initial" || phase === "restarted", "unexpected browser phase");
  for (const [key, host] of [["control","control.localhost"],["site_a","a.localhost"],["site_b","b.localhost"]]) {
    const url = new URL(fixture[key]);
    check(url.protocol === "https:" && url.hostname === host && url.port && url.origin === fixture[key], "explicit local HTTPS origins required");
  }
  check(fixture.sites?.length === 2, "missing seeded sites");
  page.setDefaultTimeout(12000);
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const result = {phase, binary_sha256:fixture.binary_sha256, checks:[]};
  const mark = value => result.checks.push(value);
  const credentials = {
    control:["control-admin","A-Strong-Control-Password-2026!"],
    site_a:["site-a-admin","A-Strong-Site-A-Password-2026!"],
    site_b:["site-b-admin","A-Strong-Site-B-Password-2026!"]
  };
  // Chromium resolves *.localhost itself; Playwright's Node request transport
  // does not. Connect only to loopback with the exact original Host/cookies.
  async function fixtureRequest(owner, method, address, options={}) {
    const target=new URL(address), original=target.origin, host=target.host;
    check(["control.localhost","a.localhost","b.localhost"].includes(target.hostname),"unexpected fixture API host");
    target.hostname="127.0.0.1";
    const headers={...options.headers,Host:host};
    if(!Object.keys(headers).some(key=>key.toLowerCase()==="cookie")) {
      const cookies=await owner.cookies(address);
      if(cookies.length) headers.Cookie=cookies.map(cookie=>cookie.name+"="+cookie.value).join("; ");
    }
    try { return await owner.request[method](target.href,{...options,headers}); }
    catch(error) { throw new Error("fixture API transport: "+String(error.message).split("\n")[0]); }
  }
  async function login(scope) {
    const origin=fixture[scope];
    await page.goto(origin+"/admin/login");
    await page.locator("form[data-login] input[name=login]").fill(credentials[scope][0]);
    await page.locator("form[data-login] input[name=password]").fill(credentials[scope][1]);
    await Promise.all([page.waitForURL(url=>url.origin===origin && url.pathname!=="/admin/login"),page.locator("form[data-login] button[type=submit]").click()]);
    await page.locator("#logout").waitFor();
  }
  async function locale(origin, tag) {
    await page.goto(origin+"/admin/language/");
    await page.locator("select[name=ui_language]").selectOption(tag);
    const [response]=await Promise.all([page.waitForNavigation({waitUntil:"domcontentloaded"}),page.locator("form:has(select[name=ui_language]) button[type=submit]").click()]);
    if(!response || response.status()!==200) {
      let detail="no response body";
      if(response) {
        const body=(await response.text()).slice(0,2048);
        try {
          const value=JSON.parse(body);
          detail=typeof value.code==="string"?"code="+value.code:"JSON response without error code";
        } catch (_) {
          detail=body.replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi,"").replace(/<[^>]*>/g,"").replace(/[\u0000-\u001f\u007f]+/g," ").replace(/\s+/g," ").trim().slice(0,240);
        }
      }
      throw new Error("locale navigation status="+(response?response.status():"none")+"; "+detail);
    }
    check(await page.locator("html").getAttribute("lang")===tag,"locale navigation200 did not select requested language");
    check(await page.locator("html").getAttribute("dir")===(tag==="en"?"ltr":"rtl"),"selected direction incorrect");
  }
  async function responsive(name) {
    await page.setViewportSize({width:390,height:844});
    const bounds=await page.evaluate(()=>({width:innerWidth,scroll:document.documentElement.scrollWidth}));
    check(bounds.scroll<=bounds.width+2,"narrow document overflow");
    const first=page.locator("main input:not([type=hidden]),main select,main button").first();
    await first.focus();
    check(await first.evaluate(element=>element===document.activeElement),"control cannot receive keyboard focus");
    await page.keyboard.press("Tab");
    check(await page.evaluate(()=>document.activeElement!==document.body),"Tab lost focus");
    if(screenshotRoot) await page.screenshot({path:screenshotRoot+"/"+phase+"-"+name+"-390.png",fullPage:true});
    await page.setViewportSize({width:1366,height:900});
    if(screenshotRoot) await page.screenshot({path:screenshotRoot+"/"+phase+"-"+name+"-wide.png",fullPage:true});
    mark(name+"-keyboard-390-wide");
  }
  // Exact released AChrix v0.2.0 title references and catalog values.
  const sharedTitles={
    media:{domain:"media",key:"library.title",fa:"کتابخانهٔ فایل‌ها",en:"File library"},
    identity:{domain:"identity",key:"accounts.title",fa:"حساب‌ها",en:"Accounts"}
  };
  async function moduleTitle(surface, tag) {
    const reference=sharedTitles[surface];
    const expected=reference[tag==="fa"?"fa":"en"];
    check(await page.evaluate(({domain,key})=>window.AChrixI18n.text(domain,key),reference)===expected,surface+" selected shared title catalog mismatch");
    check((await page.locator("h1").textContent()).trim()===expected,surface+" visible localized/fallback title mismatch");
  }
  async function noMarkup() {
    check(await page.locator("#catalog-server-probe,#catalog-browser-probe,#catalog-parts-probe,#user-title-probe").count()===0,"catalog/user text became markup");
  }

  await page.setViewportSize({width:1366,height:900});
  await page.goto(fixture.control+"/admin/login");
  check(await page.locator("html").getAttribute("lang")==="en" && await page.locator("html").getAttribute("dir")==="ltr","new browser default is not English/LTR");
  await login("control");
  await page.goto(fixture.control+"/admin/sites/");
  check((await page.locator("h1").textContent()).trim()==="Sites","English control missing");
  await responsive("control");
  await locale(fixture.control,"fa");
  await page.goto(fixture.control+"/admin/sites/");
  check((await page.locator("h1").textContent()).trim()==="سایت‌ها","Persian control missing");
  if(phase==="restarted") {
    await locale(fixture.control,"ar");
    await page.goto(fixture.control+"/admin/sites/");
    check(await page.locator("html").getAttribute("lang")==="ar","third locale unavailable on control origin");
  }
  mark("control-en-fa-third-as-applicable");

  await login("site_a");
  check(await page.locator("html").getAttribute("lang")==="en","control locale leaked to site origin");
  await locale(fixture.site_a,"fa");
  await page.goto(fixture.site_a+"/admin/content/");
  check((await page.locator("h1").textContent()).trim()==="محتوا","Persian product surface missing");
  await page.goto(fixture.site_a+"/admin/appearance/");
  check(await page.locator("select[name=site_language]").inputValue()==="fa","UI selection changed content language");
  await responsive("appearance");
  for(const surface of ["media","identity"]) {
    await page.goto(fixture.site_a+"/admin/"+surface+"/");
    check(await page.locator("html").getAttribute("lang")==="fa",surface+" lost locale");
    await moduleTitle(surface,"fa");
  }
  mark("site-fa-content-appearance-media-identity");

  const selected=phase==="initial"?"fa":"ar";
  if(phase==="restarted") {
    await locale(fixture.site_a,"ar");
    await page.goto(fixture.site_a+"/admin/content/");
    const body=await page.locator("body").textContent();
    check((await page.locator("h1").textContent()).includes('Third <img id="catalog-server-probe">'),"third catalog server title missing");
    check(body.includes("New content"),"English server fallback missing");
    check(body.includes("THIRD-ONE 1"),"installed server plural missing");
    check(await page.evaluate(()=>window.AChrixI18n.text("rixa","saved_content_count",{},1))==="THIRD-ONE 1","server/browser plural mismatch");
    check(await page.evaluate(()=>window.AChrixI18n.text("rixa","saved_content_count",{},2))==="THIRD-OTHER 2","missing category did not use other");
    check(await page.evaluate(()=>window.AChrixI18n.text("admin","completed"))==="Completed. Check the displayed result before another change.","shared English fallback missing");
    await noMarkup();
    await page.goto(fixture.site_a+"/admin/appearance/");
    check(await page.locator("select[name=site_language]").inputValue()==="fa","third UI locale rewrote content language");
    check(await page.locator("select[name=site_language] option[value=ar]").count()===0,"UI catalog changed content-language schema policy");
    for(const surface of ["media","identity"]) {
      await page.goto(fixture.site_a+"/admin/"+surface+"/");
      check(await page.locator("html").getAttribute("lang")==="ar","third locale unavailable on "+surface);
      await moduleTitle(surface,"en"); // Partial third pack deliberately uses English Module fallback.
    }
    mark("third-direction-fallback-server-browser-plural-content-separation");
  }

  await page.goto(fixture.site_a+"/admin/content/edit/"+fixture.sites[0].content_id);
  await page.waitForFunction(()=>Boolean(window.AChrixI18n));
  check(await page.locator("html").getAttribute("lang")===selected,"product navigation lost locale");
  await page.locator("input[data-media-id]").fill("bad");
  await page.locator("[data-add-media]").click();
  const editorError=await page.evaluate(()=>window.AChrixI18n.text("rixa","editor_invalid_media"));
  check(await page.locator("#status").textContent()===editorError,"actual editor validation did not use catalog");
  await noMarkup();
  const title='Browser فارسی English <img id="user-title-probe"> '+phase;
  await page.locator("[data-content-save] input[name=title]").fill(title);
  await page.locator("input[data-media-id]").fill("");
  const before=await page.locator("[data-current-revision]").textContent();
  const operation=await page.locator("[data-content-save] input[name=operation_id]").inputValue();
  await page.locator("[data-content-save] button[type=submit]").click();
  await page.waitForFunction(before=>document.querySelector("[data-current-revision]").textContent!==before,before);
  const lookup=page.locator("[data-operation-lookup]");
  await lookup.locator("input[name=operation_id]").fill(operation);
  await lookup.locator("button[type=submit]").click();
  await page.waitForFunction(operation=>document.querySelector("#status").textContent.includes(operation),operation);
  check(await page.locator("#status bdi[dir=ltr]").count()===4,"named mutation result did not isolate opaque values");
  await noMarkup();
  mark("actual-save-lookup-named-parameters-escaping");

  // Commit once at the server, then lose its acknowledgement. The normal
  // browser flow must inspect that operation and preserve user input.
  const uncertain=await page.locator("[data-content-save] input[name=operation_id]").inputValue();
  let intercepted=0;
  await page.route("**/admin/content/save",async route=>{intercepted++;const request=route.request(),target=new URL(request.url()),host=target.host;target.hostname="127.0.0.1";try {const response=await route.fetch({url:target.href,headers:{...request.headers(),host},maxRedirects:0});await response.dispose();} catch(error) {await route.abort("failed");throw new Error("fixture acknowledgement transport: "+String(error.message).split("\n")[0]);}await route.abort("failed");},{times:1});
  await page.locator("[data-content-save] button[type=submit]").click();
  await page.waitForFunction(operation=>document.querySelector("#status").textContent.includes(operation),uncertain);
  check(intercepted===1,"unknown mutation submitted more than once");
  check(await page.locator("[data-content-save] input[name=operation_id]").inputValue()===uncertain,"unknown mutation rotated operation");
  check(await lookup.locator("input[name=operation_id]").inputValue()===uncertain,"unknown mutation did not prepare inspection");
  check(await page.locator("[data-content-save] input[name=title]").inputValue()===title,"unknown mutation lost input");
  await lookup.locator("button[type=submit]").click();
  await page.waitForFunction(operation=>document.querySelector("#status").textContent.includes(operation)&&document.querySelectorAll("#status bdi").length===4,uncertain);
  await noMarkup();
  await page.reload();
  check(await page.locator("[data-content-save] input[name=title]").inputValue()===title,"recovery lost committed title");
  await responsive("editor");
  mark("lost-acknowledgement-inspect-no-blind-replay");

  const csrfResponse=await fixtureRequest(context,"get",fixture.site_a+"/auth/csrf",{headers:{"Origin":fixture.site_a,"X-Identity-Request":"1"}});
  check(csrfResponse.status()===200,"CSRF request denied");
  const token=(await csrfResponse.json()).csrf;
  const current=await page.locator("[data-current-revision]").textContent();
  const badLocale=await fixtureRequest(context,"get",fixture.site_a+"/admin/content/?ui_language=not-installed",{maxRedirects:0});
  check(badLocale.status()===400,"unsupported locale did not return400");
  const denied=await fixtureRequest(context,"post",fixture.site_a+"/admin/content/save",{
    headers:{"Origin":fixture.site_b,"X-Identity-Request":"1","X-CSRF-Token":token},
    data:{operation_id:"10000000-0000-4000-8000-000000000001",id:fixture.sites[0].content_id,expected_head:current,title:"must-not-write",body_html:"<p>denied</p>"}
  });
  check([401,403].includes(denied.status()),"foreign Origin reached mutation");
  await page.reload();
  check(await page.locator("[data-current-revision]").textContent()===current,"denial changed revision");
  check(await page.locator("[data-content-save] input[name=title]").inputValue()===title,"denial changed title");
  const cookies=await context.cookies(fixture.site_a+"/admin/");
  const identity=cookies.find(cookie=>cookie.httpOnly&&cookie.name!=="rixa-ui-language");
  const preference=cookies.find(cookie=>cookie.name==="rixa-ui-language");
  check(identity&&preference&&preference.domain==="a.localhost"&&preference.secure&&preference.httpOnly&&preference.sameSite==="Lax"&&preference.path==="/admin","preference cookie scope weakened");
  const foreignCookie=await fixtureRequest(context,"post",fixture.site_b+"/admin/content/create",{
    headers:{"Cookie":identity.name+"="+identity.value,"Origin":fixture.site_b,"X-Identity-Request":"1","X-CSRF-Token":token},
    data:{operation_id:"10000000-0000-4000-8000-000000000002",kind:"post",title:"denied",body_html:"<p>denied</p>"}
  });
  check([401,403].includes(foreignCookie.status()),"foreign site cookie accepted");
  mark("unsupported-locale-origin-cookie-denials");

  const anonymous=await browser.newContext({ignoreHTTPSErrors:true});
  try {
    for(const path of [fixture.sites[0].preview_path,"/admin/media/read/"+fixture.sites[0].asset_id]) {
      const response=await fixtureRequest(anonymous,"get",fixture.site_a+path,{maxRedirects:0});
      check([303,401,403].includes(response.status()),"anonymous private resource not denied");
      const body=await response.text();
      check(!body.includes("Baseline فارسی")&&!body.includes("Retained site-a")&&!body.includes(title),"anonymous response leaked private bytes");
    }
    const published=await fixtureRequest(anonymous,"get",fixture.site_a+fixture.sites[0].public_path);
    check(published.status()===200&&(await published.text()).includes("Retained site-a"),"anonymous public content unavailable");
  } finally {await anonymous.close();}
  await login("site_b");
  const foreign=await fixtureRequest(context,"get",fixture.site_b+"/admin/content/edit/"+fixture.sites[0].content_id,{maxRedirects:0});
  check(foreign.status()===404,"foreign content escaped site scope");
  await page.locator("#logout").click();
  await page.waitForURL(url=>url.pathname==="/admin/login");
  const revoked=await fixtureRequest(context,"get",fixture.site_b+"/admin/content/",{maxRedirects:0});
  check([303,401,403].includes(revoked.status()),"logged-out session retained access");
  mark("anonymous-private-public-foreign-content-logout");
  check(errors.length===0,"page JavaScript errors: "+errors.join("; "));
  return result;
}
