// SPDX-License-Identifier: MPL-2.0
"use strict";

// Execute only against the owned guest forward. The caller loads credentials
// privately and supplies a durable checkpoint writer; this file contains none.
async function rixaNativeUpdateBrowser({context,page,fixture,checkpoint,screenshotRoot}) {
  const check=(value,message)=>{if(!value)throw new Error(message);};
  check(fixture?.schema==="rixa.native-update-browser.v1","unexpected browser fixture");
  check(typeof checkpoint==="function","durable private checkpoint writer required");
  const origins=[fixture.control_origin,fixture.site_origin];
  check(origins[0]==="https://control.rixa.test"&&origins[1]==="https://site.rixa.test","only exact guest origins allowed");
  check(fixture.forward==="https://127.0.0.1:22443","only owned loopback guest forward allowed");
  const checks=[],errors=[];
  page.setDefaultTimeout(20000);
  page.on("pageerror",error=>errors.push(error.name));
  // Browser origin/cookie/SameSite/CSRF behavior stays on the real product
  // origins. Only this ephemeral context's transport uses the owned loopback
  // forward. No host DNS/network mutation or shared browser profile is needed.
  // The caller explicitly scopes ignoreHTTPSerrors; strict CA/TLS is verified
  // separately inside the guest, not claimed from this transport exception.
  await context.route("**/*",async route=>{
    const request=route.request(),url=new URL(request.url());
    check(origins.includes(url.origin),"unexpected external browser request");
    const headers=await request.allHeaders();
    headers.host=url.host;
    const response=await route.fetch({url:fixture.forward+url.pathname+url.search,
      headers,maxRedirects:0,timeout:25000});
    await route.fulfill({response});
  });
  async function action(name,run) {
    await checkpoint({action:name,state:"attempted"});
    await run();
    await checkpoint({action:name,state:"acknowledged"});
  }
  async function screenshot(name) {
    if(screenshotRoot) await page.screenshot({path:screenshotRoot+"/"+name+".png",fullPage:true});
  }
  async function layout(name) {
    for(const width of [390,1366]) {
      await page.setViewportSize({width,height:900});
      check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+2),"narrow page overflow");
      const first=page.locator("main input:not([type=hidden]),main select,main button").first();
      await first.focus();
      check(await first.evaluate(e=>document.activeElement===e),"control focus failed");
      await page.keyboard.press("Tab");
      check(await page.evaluate(()=>document.activeElement!==document.body),"keyboard focus lost");
      await screenshot(name+"-"+width);
    }
    checks.push(name+"-keyboard-390-wide");
  }
  async function login(scope) {
    const origin=fixture[scope+"_origin"],credentials=fixture[scope+"_credentials"];
    await page.goto(origin+"/admin/login");
    await page.locator("form[data-login] input[name=login]").fill(credentials.login);
    await page.locator("form[data-login] input[name=password]").fill(credentials.password);
    await action(scope+"-login",async()=>{
      await Promise.all([page.waitForURL(url=>url.origin===origin&&url.pathname!=="/admin/login"),
        page.locator("form[data-login] button[type=submit]").click()]);
      await page.locator("#logout").waitFor();
    });
  }
  async function language(scope,tag) {
    const origin=fixture[scope+"_origin"];
    await page.goto(origin+"/admin/language/");
    await page.locator("select[name=ui_language]").selectOption(tag);
    await action(scope+"-language-"+tag,async()=>{
      const [response]=await Promise.all([page.waitForNavigation({waitUntil:"domcontentloaded"}),
        page.locator("form:has(select[name=ui_language]) button[type=submit]").click()]);
      check(response?.status()===200,"language selection failed");
      check(await page.locator("html").getAttribute("lang")===tag,"selected language missing");
      check(await page.locator("html").getAttribute("dir")===(tag==="fa"?"rtl":"ltr"),"selected direction missing");
    });
  }
  await page.setViewportSize({width:1366,height:900});
  await page.goto(fixture.control_origin+"/admin/login");
  check(await page.locator("html").getAttribute("lang")==="en","new browser default is not English");
  check(await page.locator("html").getAttribute("dir")==="ltr","new browser default is not LTR");
  await login("control");
  await page.goto(fixture.control_origin+"/admin/sites/");
  check((await page.locator("h1").textContent()).trim()==="Sites","control English title missing");
  await layout("control-en");
  await language("control","fa");
  await page.goto(fixture.control_origin+"/admin/sites/");
  check((await page.locator("h1").textContent()).trim()==="سایت‌ها","control Persian title missing");
  await layout("control-fa");
  await login("site");
  check(await page.locator("html").getAttribute("lang")==="en","control language leaked to site");
  await page.goto(fixture.site_origin+"/admin/content/");
  check((await page.locator("h1").textContent()).trim()==="Content","site English content title missing");
  await layout("content-en");
  await language("site","fa");
  await page.goto(fixture.site_origin+"/admin/content/");
  check((await page.locator("h1").textContent()).trim()==="محتوا","site Persian content title missing");
  await layout("content-fa");
  await page.goto(fixture.site_origin+"/admin/appearance/");
  check(await page.locator("select[name=site_language]").inputValue()==="fa","UI selection changed content language");
  await layout("appearance-fa");
  for(const [path,title] of [["media","کتابخانهٔ فایل‌ها"],["identity","حساب‌ها"]]) {
    await page.goto(fixture.site_origin+"/admin/"+path+"/");
    check(await page.locator("html").getAttribute("lang")==="fa","shared component lost locale");
    check((await page.locator("h1").textContent()).trim()===title,"shared component title not localized");
    await screenshot(path+"-fa");
  }
  await page.goto(fixture.site_origin+"/admin/content/preview/"+fixture.content_id+"/"+fixture.revision);
  check((await page.locator("body").textContent()).includes("Retained native guest publication proof."),"retained browser preview missing");
  await page.goto(fixture.site_origin+fixture.public_route);
  check((await page.locator("body").textContent()).includes("Retained native فارسی"),"retained browser publication missing");
  const image=page.locator('img[src="'+fixture.public_asset_path+'"]');
  check(await image.count()===1,"published Media image missing");
  await image.waitFor();
  check(await image.evaluate(e=>e.complete&&e.naturalWidth>0),"browser failed to decode retained Media");
  await screenshot("retained-public");
  checks.push("retained-content-media-preview-public","content-language-independent","shared-components-fa");
  for(const scope of ["control","site"]) {
    const origin=fixture[scope+"_origin"];
    await page.goto(origin+"/admin");
    await action(scope+"-logout",async()=>{
      await Promise.all([page.waitForURL(url=>url.origin===origin&&url.pathname==="/admin/login"),
        page.locator("#logout").click()]);
    });
    check((await context.cookies(origin)).every(cookie=>cookie.name!=="__Host-AChrix-Session"),"known browser session cookie remains");
  }
  check(errors.length===0,"browser JavaScript errors");
  return {status:"verified",checks,scope:"owned-guest-ephemeral-loopback-routing",
    tls:"context ignoreHTTPSerrors; independent strict guest CA/TLS proof required",javascript_errors:errors.length};
}
