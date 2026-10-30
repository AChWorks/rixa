// SPDX-License-Identifier: MPL-2.0
"use strict";

// Source-only fake-native preview; never point this driver at a real console.
// Caller reads the private marker and supplies allowlisted checkpoint/control I/O.
async function rixaManageWebBrowser({context,page,fixture,checkpoint,control,mode="journey",screenshotRoot}) {
 const check=(ok,message)=>{if(!ok)throw new Error(message);};
 check(fixture?.schema==="rixa.manageweb-preview.v1","unexpected fake preview schema");
 const origin=new URL(fixture.origin);
 check(origin.protocol==="https:"&&origin.hostname==="127.0.0.1"&&!origin.username&&!origin.password&&!origin.search&&origin.pathname==="/","only loopback TLS fake preview allowed");
 check(fixture.operation==="fixture-install"&&typeof fixture.permit==="string"&&fixture.permit.length===43,"unexpected fake preview identity");
 check(typeof checkpoint==="function"&&typeof control==="function","private checkpoint/control callbacks required");
 check(["journey","expiry"].includes(mode),"unsupported bounded browser mode");
 const checks=[],errors=[],counts={configure:0,apply:0,review:0,status:0};
 const configureBodies=[];
 let reviewedToken="",applyUsesReview=false;
 page.setDefaultTimeout(10000);
 page.on("pageerror",error=>errors.push(error.name));
 await context.route("**/*",async route=>{
  const request=route.request(),url=new URL(request.url());
  check(url.origin===origin.origin&&!url.search,"unexpected browser destination");
  const action=url.pathname.split("/").pop();
  if(url.pathname.startsWith("/admin/operation/")&&Object.hasOwn(counts,action))counts[action]++;
  if(request.method()==="POST"&&action==="review") {
   const response=await route.fetch();
   const body=await response.json();
   reviewedToken=body.review_token||"";
   await route.fulfill({response});
   return;
  }
  if(request.method()==="POST"&&action==="configure") {
   configureBodies.push(request.postData());
   if(mode==="journey"&&counts.configure===1) {
    const response=await route.fetch();
    check(response.status()===200,"fake configure did not commit before response loss");
    await route.abort("failed"); return;
   }
  }
  if(request.method()==="POST"&&action==="apply") {
   applyUsesReview=JSON.parse(request.postData()).review_token===reviewedToken&&reviewedToken.length===64;
   const response=await route.fetch();
   check(response.status()===200,"fake apply did not commit before response loss");
   await route.abort("failed"); return;
  }
  await route.continue();
 });
 async function step(name,run) {
  await checkpoint({phase:name,status:"attempted",counts:{...counts}});
  await run();
  checks.push(name);
  await checkpoint({phase:name,status:"passed",counts:{...counts}});
 }
 async function noStorage() {
  check(await page.evaluate(()=>localStorage.length===0&&sessionStorage.length===0),"WebStorage was populated");
 }
 async function screenshot(name) {
  if(screenshotRoot)await page.screenshot({path:screenshotRoot+"/"+name+".png",fullPage:true});
 }
 async function layout(name) {
  for(const width of [390,1366]) {
   await page.setViewportSize({width,height:900});
   check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),"horizontal overflow");
   const select=page.locator("[data-language]");
   await select.focus();
   await page.keyboard.press("Tab");
   check(await page.evaluate(()=>document.activeElement!==document.body&&getComputedStyle(document.activeElement).outlineStyle!=="none"),"keyboard focus indicator missing");
   await screenshot(name+"-"+width);
  }
 }
 async function language(tag,direction) {
  await Promise.all([page.waitForNavigation({waitUntil:"domcontentloaded"}),page.locator("[data-language]").selectOption(tag)]);
  check(await page.locator("html").getAttribute("lang")===tag,"selected locale absent");
  check(await page.locator("html").getAttribute("dir")===direction,"selected locale direction absent");
  await page.locator("[data-inspect]").waitFor();
  await page.waitForFunction(()=>!document.querySelector("[data-inspect]").disabled);
 }
 const secrets={control_dsn:"postgres://control:browser-canary-control@127.0.0.1:5432/control?sslmode=disable",
  site_dsn:"postgres://site:browser-canary-site@127.0.0.1:5432/site?sslmode=disable",
  control_password:"browser-canary-control-password",site_password:"browser-canary-site-password"};
 const form=()=>page.locator("[data-configure]");
 await step("permit-login-cookie-headers",async()=>{
  const response=await page.goto(origin.origin+"/access");
  check(response.status()===200,"access page unavailable");
  const headers=response.headers();
  check(headers["cache-control"]==="no-store"&&headers["referrer-policy"]==="no-referrer"&&headers["content-security-policy"].includes("frame-ancestors 'none'"),"privacy headers absent");
  check(await page.locator("html").getAttribute("lang")==="en"&&await page.locator("html").getAttribute("dir")==="ltr","English/LTR default absent");
  await page.locator("[name=permit]").fill(fixture.permit);
  await Promise.all([page.waitForURL(origin.origin+"/admin/operation/"),page.locator("[data-permit] button").click()]);
  await page.waitForFunction(()=>!document.querySelector("[data-inspect]").disabled);
  const cookies=await context.cookies(origin.origin),session=cookies.find(x=>x.name.startsWith("__Host-"));
  check(session?.secure&&session.httpOnly&&session.sameSite==="Strict"&&session.path==="/","private independent session attributes absent");
  check(!session.value.includes(fixture.permit),"permit reused as session");
  check(!page.url().includes(fixture.permit),"permit leaked in URL");
  const denied=await page.evaluate(async()=>{
   const response=await fetch("/admin/operation/review",{method:"POST",credentials:"same-origin",headers:{"X-Identity-Request":"1","X-CSRF-Token":"invalid","Content-Type":"application/json"},body:"{}"});
   return response.status;
  });
  check(denied===401,"browser mutation accepted an invalid CSRF token");
  await noStorage();
 });
 if(mode==="journey") {
  await step("en-fa-installed-locale-keyboard-layout",async()=>{
   await layout("console-en");
   await language("fa","rtl"); await layout("console-fa");
   await language("ar","rtl");
   check((await page.locator("h1").textContent()).includes("Installed <text>"),"installed locale/fallback missing");
   check(await page.locator("h1 text").count()===0,"catalog text became markup");
   await layout("console-installed");
   await language("en","ltr");
  });
 }
 await step("dirty-input-navigation-retention",async()=>{
  await form().locator("[name=control_origin]").fill("https://control.example.invalid:9443");
  await form().locator("[name=control_password]").fill(secrets.control_password);
  check(await page.locator("[data-action=review]").isDisabled(),"dirty review is enabled");
  let dialogs=0;
  const dismiss=async dialog=>{dialogs++;await dialog.dismiss();};
  page.once("dialog",dismiss);
  await page.locator("[data-language]").selectOption("fa");
  check(dialogs===1&&await page.locator("[data-language]").inputValue()==="en","dirty navigation was not cancelled");
  check(await form().locator("[name=control_password]").inputValue()===secrets.control_password,"dirty secret lost");
  await noStorage();
 });
 if(mode==="expiry") {
  await step("actual-expiry-retains-input-without-unknown-mutation",async()=>{
   for(const [name,value]of Object.entries({...secrets,site_origin:"https://site.example.invalid:9443",listen:"0.0.0.0:9443",control_login:"control-admin",site_login:"site-admin"}))await form().locator("[name="+name+"]").fill(value);
   await control("expire");
   await form().locator("button").click();
   await page.waitForFunction(()=>document.querySelector("#status").getAttribute("role")==="alert");
   check(await page.locator("[data-pending]").isHidden(),"CSRF expiry became unknown mutation");
   check(counts.configure===0,"expired session sent configure");
   check(await form().locator("[name=control_password]").inputValue()===secrets.control_password,"expiry discarded unsaved secret");
   check((await context.request.get(origin.origin+"/console-auth/csrf",{headers:{"X-Identity-Request":"1"}})).status()===401,"server session still live");
   await noStorage();
  });
 } else {
  await step("private-fake-upload",async()=>{
   await page.locator("details:has(> summary):has(> form[data-upload]) > summary").click();
   const upload=page.locator("[data-upload]");
   await upload.locator("[name=release]").fill("v1.2.3");
   await upload.locator("[name=source_commit]").fill("a".repeat(40));
   for(const name of ["archive","bundle"])await upload.locator("[name="+name+"]").setInputFiles({name:"fake-"+name,mimeType:"application/octet-stream",buffer:Buffer.from("fake fixture only")});
   await upload.locator("button").click();
   await page.waitForFunction(()=>document.querySelector("[data-artifact]").textContent.includes("v1.2.3"));
  });
  await step("lost-configure-locked-payload",async()=>{
   for(const [name,value]of Object.entries({...secrets,site_origin:"https://site.example.invalid:9443",listen:"0.0.0.0:9443",control_login:"control-admin",site_login:"site-admin"}))await form().locator("[name="+name+"]").fill(value);
   await form().locator("button").click();
   await page.locator("[data-pending]").waitFor({state:"visible"});
   check(await form().locator("[name=control_origin]").isDisabled(),"unknown configure is editable");
   check(await page.locator("[data-retry]").isHidden(),"retry available before inspection");
   check(await form().locator("[name=control_password]").inputValue()===secrets.control_password,"unknown configure lost exact input");
  });
  await step("readonly-failure-keeps-pending",async()=>{
   await control("inspection-unavailable");
   await page.locator("[data-inspect]").click();
   await page.waitForFunction(()=>!document.querySelector("[data-inspect]").disabled);
   check(await page.locator("[data-pending]").isVisible()&&await page.locator("[data-retry]").isHidden(),"failed inspection unlocked unknown action");
   check(counts.configure===1,"inspection replayed configure");
   await control("restore-inspection");
  });
  await step("inspect-exact-configure-retry-clears-secrets",async()=>{
   await page.locator("[data-inspect]").click();
   await page.locator("[data-retry]").waitFor({state:"visible"});
   await page.locator("[data-retry]").click();
   await page.locator("[data-pending]").waitFor({state:"hidden"});
   check(counts.configure===2&&configureBodies[0]===configureBodies[1],"configure retry changed exact body");
   for(const name of Object.keys(secrets))check(await form().locator("[name="+name+"]").inputValue()==="","acknowledged secret remains in input");
   check(await form().locator("[name=control_origin]").inputValue()==="https://control.example.invalid:9443","valid input was discarded");
  });
  await step("readonly-failure-is-not-unknown",async()=>{
   await control("inspection-unavailable");
   await page.locator("[data-inspect]").click();
   await page.waitForFunction(()=>!document.querySelector("[data-inspect]").disabled);
   check(await page.locator("[data-pending]").isHidden(),"read-only failure created pending mutation");
   await control("restore-inspection");
   await page.locator("[data-inspect]").click();
   await page.waitForFunction(()=>!document.querySelector("[data-inspect]").disabled);
  });
  await step("saved-input-language-navigation",async()=>{
   for(const tag of ["fa","en"]) {
    await language(tag,tag==="fa"?"rtl":"ltr");
    check(await form().locator("[name=control_origin]").inputValue()==="https://control.example.invalid:9443","saved input lost across locale navigation");
    for(const name of Object.keys(secrets))check(await form().locator("[name="+name+"]").inputValue()===""&&await form().locator("[name="+name+"]").evaluate(e=>!e.required),"saved secret was returned or required again");
    check(!(await page.content()).includes("browser-canary"),"saved secret leaked in HTML");
   }
  });
  await step("review-redaction-explicit-confirmation",async()=>{
   await page.locator("[data-action=review]").click();
   await page.locator("[data-review]").waitFor({state:"visible"});
   const review=await page.locator("[data-review]").textContent();
   check(!review.includes("browser-canary")&&review.includes("postgres://control@127.0.0.1"),"review leaked a credential or lacks redaction");
   const apply=page.locator("[data-action-form=apply]");
   await apply.locator("button").click();
   check(counts.apply===0&&await apply.locator("[name=confirm_apply]").evaluate(e=>!e.validity.valid),"Apply bypassed explicit confirmation");
   await apply.locator("[name=confirm_apply]").check();
   await page.locator("[data-action=review]").click();
   await page.waitForFunction(()=>!document.querySelector("[data-action=review]").disabled);
   check(!(await apply.locator("[name=confirm_apply]").isChecked()),"fresh review retained old confirmation");
   await screenshot("console-reviewed");
  });
  await step("lost-apply-inspect-same-operation-no-replay",async()=>{
   const apply=page.locator("[data-action-form=apply]");
   await apply.locator("[name=confirm_apply]").check();
   await apply.locator("button").click();
   await page.locator("[data-pending]").waitFor({state:"visible"});
   check(counts.apply===1&&applyUsesReview,"Apply did not use exact review token");
   await page.locator("[data-inspect]").click();
   await page.locator("[data-pending]").waitFor({state:"hidden"});
   check((await page.locator("[data-code]").textContent()).includes("applying"),"same operation applying state missing");
   check(await page.locator("[data-retry]").isHidden()&&counts.apply===1,"Apply is replayable after applying inspection");
   await noStorage();
  });
  await step("actual-completion-lockout",async()=>{
   await control("installed");
   await page.locator("[data-inspect]").click();
   await page.waitForFunction(()=>document.querySelector("[data-code]").textContent.includes("installed"));
   check((await context.request.get(origin.origin+"/console-auth/csrf",{headers:{"X-Identity-Request":"1"}})).status()===401,"completion did not revoke session");
   check(await page.locator("[data-config]").isHidden(),"completion left configure available");
   check(counts.apply===1,"completion replayed Apply");
   await noStorage();
  });
 }
 check(errors.length===0,"browser JavaScript errors");
 const result={status:"verified",mode,checks,counts,javascript_errors:errors.length,
  scope:"source-only-loopback-TLS-fake-native",tls:"explicit self-signed context exception; strict TLS is separate evidence"};
 await checkpoint({phase:"complete",status:"passed",...result});
 return result;
}
