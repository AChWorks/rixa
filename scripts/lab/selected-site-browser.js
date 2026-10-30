// SPDX-License-Identifier: MPL-2.0
"use strict";

// One explicit isolated real-binary journey. privateRoot is the caller-owned
// fixture directory; the journal is exclusive and fsynced before mutation.
async function rixaSelectedSiteBrowser({browser, context, page, fixture, privateRoot}) {
  const check=(value,message)=>{if(!value) throw new Error(message);};
  check(fixture?.schema==="rixa.selected-site-browser.v1","unexpected selected-site fixture");
  const origins={control:"control.localhost",site_a:"a.localhost",site_b:"b.localhost"};
  for(const [name,host] of Object.entries(origins)) {
    const parsed=new URL(fixture[name]);
    check(parsed.protocol==="https:"&&parsed.hostname===host&&parsed.port&&parsed.origin===fixture[name],"only declared local HTTPS origins are accepted");
  }
  check(fixture.sites?.length===2&&fixture.sites[0].id==="site-a"&&fixture.sites[1].id==="site-b"&&fixture.sites[0].origin===fixture.site_a&&fixture.sites[1].origin===fixture.site_b,"two exact seeded sites required");
  const fs=await import("node:fs/promises");
  const https=await import("node:https");
  const rootInfo=await fs.lstat(privateRoot);
  check(rootInfo.isDirectory()&&!rootInfo.isSymbolicLink()&&(rootInfo.mode&0o077)===0&&await fs.realpath(privateRoot)===privateRoot,"private journal root must be an existing canonical0700 directory");
  const journal=await fs.open(privateRoot+"/selected-site-attempts.jsonl","wx",0o600);
  let queue=Promise.resolve();
  const record=entry=>queue=queue.then(async()=>{await journal.write(JSON.stringify({at:new Date().toISOString(),...entry})+"\n");await journal.sync();});
  await record({type:"header",binary_sha256:fixture.binary_sha256});
  const result={binary_sha256:fixture.binary_sha256,checks:[]};
  const mark=value=>result.checks.push(value);
  const errors=[], routeErrors=[];
  page.setDefaultTimeout(15000);
  page.on("pageerror",error=>errors.push(error.message));
  context.on("page",p=>p.on("pageerror",error=>errors.push(error.message)));
  // Chromium resolves *.localhost itself; Node APIRequest/route.fetch may
  // not. Dial only the declared loopback endpoint, preserving Host/SNI,
  // Origin, cookies, CSRF and exact request bytes. No host DNS change is made.
  async function fixtureRequest(url, options={}, jar=context) {
    const target=new URL(url);
    check(Object.keys(origins).some(key=>fixture[key]===target.origin),"fixture HTTP dial left declared origins");
    const headers={};
    for(const [name,value] of Object.entries(options.headers||{})) headers[name.toLowerCase()]=value;
    headers.host=target.host;
    if(!Object.hasOwn(headers,"cookie")) headers.cookie=(await jar.cookies(target.origin)).map(c=>c.name+"="+c.value).join("; ");
    let body=options.data===undefined?undefined:typeof options.data==="string"?options.data:JSON.stringify(options.data);
    if(body!==undefined) {
      if(!headers["content-type"]) headers["content-type"]="application/json";
      headers["content-length"]=Buffer.byteLength(body);
    }
    return new Promise((resolve,reject)=>{
      const request=https.request({hostname:"127.0.0.1",port:Number(target.port),servername:target.hostname,rejectUnauthorized:false,
        method:options.method||"GET",path:target.pathname+target.search,headers},response=>{
        const chunks=[];let size=0;
        response.on("data",chunk=>{size+=chunk.length;if(size>2*1024*1024){request.destroy(new Error("fixture response exceeded bound"));return;}chunks.push(chunk);});
        response.on("error",reject);
        response.on("end",()=>{
          const bytes=Buffer.concat(chunks),fulfillHeaders={};
          for(const [name,value] of Object.entries(response.headers)) {
            if(typeof value==="string"&&!["content-length","transfer-encoding","connection"].includes(name)) fulfillHeaders[name]=value;
          }
          resolve({status:()=>response.statusCode,rawHeaders:response.headers,fulfillHeaders,bytes,
            text:async()=>bytes.toString("utf8"),json:async()=>JSON.parse(bytes.toString("utf8"))});
        });
      });
      request.setTimeout(10000,()=>request.destroy(new Error("fixture scoped HTTP timeout")));
      request.on("error",reject);if(body!==undefined) request.write(body);request.end();
    });
  }
  const api={get:(url,options)=>fixtureRequest(url,{...options,method:"GET"}),post:(url,options)=>fixtureRequest(url,{...options,method:"POST"})};
  let fault="none", faults=0, previousUnknownBody="";
  const routePattern=/\/admin\/sites\/site\/(site-a|site-b)\/content\/(create|save)$/;
  const handler=async route=>{
    try {
      const request=route.request();
      if(request.method()!=="POST") return route.continue();
      check(new URL(request.url()).origin===fixture.control,"mutation left the declared control origin");
      const match=new URL(request.url()).pathname.match(routePattern);
      const payload=request.postData();
      check(match&&typeof payload==="string"&&payload.length<=96*1024,"mutation exceeded fixture journal contract");
      const value=JSON.parse(payload);
      check(typeof value.operation_id==="string","mutation operation ID missing");
      await record({type:"mutation",site_id:match[1],path:new URL(request.url()).pathname,payload});
      const selectedFault=fault; fault="none";
      if(selectedFault==="never_sent") {
        faults++;previousUnknownBody=payload;
        await record({type:"transport",site_id:match[1],operation_id:value.operation_id,outcome:"not_sent"});
        return route.abort("failed");
      }
      if(selectedFault==="retry_exact") check(payload===previousUnknownBody,"explicit retry changed frozen serialized payload");
      const response=await fixtureRequest(request.url(),{method:request.method(),headers:await request.allHeaders(),data:payload});
      await record({type:"outcome",site_id:match[1],operation_id:value.operation_id,status:response.status()});
      if(selectedFault==="commit_then_abort") {
        check(response.status()===200,"suppressed acknowledgement was not a successful real mutation");
        faults++;
        return route.abort("failed");
      }
      return route.fulfill({status:response.status(),headers:response.fulfillHeaders,body:response.bytes});
    } catch(error) {
      routeErrors.push(error.message);
      await record({type:"transport_error",message:String(error.message).slice(0,500)});
      await route.abort("failed").catch(()=>{});
    }
  };
  await context.route(routePattern,handler);
  const credentials={control:["control-admin","A-Strong-Control-Password-2026!"],site_a:["site-a-admin","A-Strong-Site-A-Password-2026!"]};
  async function login(target,scope="control") {
    const origin=fixture[scope];
    await target.goto(origin+"/admin/login");
    await target.locator("form[data-login] input[name=login]").fill(credentials[scope][0]);
    await target.locator("form[data-login] input[name=password]").fill(credentials[scope][1]);
    await Promise.all([target.waitForURL(url=>url.origin===origin&&url.pathname!=="/admin/login"),target.locator("form[data-login] button[type=submit]").click()]);
    await target.locator("#logout").waitFor();
  }
  const base=site=>"/admin/sites/site/"+site.id+"/content";
  async function scope(site) {
    const root=page.locator("[data-content-workspace]");
    check(await root.getAttribute("data-site-id")===site.id&&await root.getAttribute("data-origin")===site.origin,"visible workspace bound to wrong site");
    const label=await page.locator(".selected-site").textContent();
    check(label.includes(site.id)&&label.includes(site.origin),"selected ID/origin not visible");
    check(await page.locator("input[name=site_id]").count()===0,"caller site ID appeared in mutation body");
    check(await page.locator("[data-publication-form],[data-publication-apply],[data-publication-clear]").count()===0,"selected draft workspace exposed publication");
  }
  async function locale(tag) {
    await page.goto(fixture.control+"/admin/language/");
    await page.locator("select[name=ui_language]").selectOption(tag);
    const [response]=await Promise.all([page.waitForNavigation({waitUntil:"domcontentloaded"}),page.locator("form:has(select[name=ui_language]) button[type=submit]").click()]);
    check(response?.status()===200,"locale navigation status="+response?.status());
    check(await page.locator("html").getAttribute("lang")===tag&&await page.locator("html").getAttribute("dir")===(tag==="en"?"ltr":"rtl"),"locale metadata not served");
  }
  async function responsive(name) {
    await page.setViewportSize({width:390,height:844});
    check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+2),"390px document overflow");
    const first=page.locator("[data-content-workspace] input:not([type=hidden]),[data-content-workspace] select,[data-content-workspace] button").first();
    await first.focus();check(await first.evaluate(e=>e===document.activeElement),"keyboard focus missing");
    await page.keyboard.press("Tab");check(await page.evaluate(()=>document.activeElement!==document.body),"Tab lost focus");
    await page.screenshot({path:privateRoot+"/"+name+"-390.png",fullPage:true});
    await page.setViewportSize({width:1366,height:900});
    await page.screenshot({path:privateRoot+"/"+name+"-wide.png",fullPage:true});
    mark(name+"-keyboard-390-wide");
  }
  async function csrf() {
    const response=await api.get(fixture.control+"/auth/csrf",{headers:{"Origin":fixture.control,"X-Identity-Request":"1"}});
    check(response.status()===200,"control CSRF unavailable");
    return (await response.json()).csrf;
  }
  async function apiMutation(site,path,payload,expected=200,type="mutation") {
    const body=JSON.stringify(payload);
    await record({type,site_id:site.id,path,payload:body});
    const response=await api.post(fixture.control+path,{headers:{"Origin":fixture.control,"X-Identity-Request":"1","X-CSRF-Token":await csrf(),"Content-Type":"application/json"},data:body});
    await record({type:"outcome",site_id:site.id,operation_id:payload.operation_id,status:response.status()});
    check(response.status()===expected,"scoped API mutation status="+response.status()+" expected="+expected);
    return response;
  }
  async function submitSave() {
    const before=await page.locator("[data-current-revision]").textContent();
    await page.locator("[data-content-save] button[type=submit]").click();
    await page.waitForFunction(before=>document.querySelector("[data-current-revision]").textContent!==before,before);
    check(routeErrors.length===0,"request journal/transport failed: "+routeErrors.join("; "));
  }
  async function discardAndGoto(url) {
    const accept=dialog=>dialog.accept();
    page.on("dialog",accept);
    try {await page.goto(url);} finally {page.off("dialog",accept);}
  }
  async function cancelledNavigation() {
    const before=page.url(),title=await page.locator("[data-content-save] input[name=title]").inputValue();
    const dialog=page.waitForEvent("dialog");
    const click=page.locator("a[href='/admin/sites'],a[href='/admin/sites/']").first().click();
    const prompt=await dialog;check(prompt.type()==="confirm","product navigation guard did not prompt");await prompt.dismiss();await click;
    check(page.url()===before&&await page.locator("[data-content-save] input[name=title]").inputValue()===title,"cancelled navigation lost draft/scope");
  }
  try {
    await page.setViewportSize({width:1366,height:900});
    await login(page);
    check((await context.cookies(fixture.site_a)).length===0,"control login created a site cookie");
    await page.goto(fixture.control+"/admin/sites/");
    for(const site of fixture.sites) {
      const link=page.locator("a[href='"+base(site)+"/']");
      check(await link.count()===1,"Sites inventory lacks explicit workspace link");
    }
    mark("control-only-inventory-selection");
    for(const site of fixture.sites) {
      await page.goto(fixture.control+base(site)+"/");
      await scope(site);
      const ids=new Set();let pages=0;
      for(;;) {
        pages++;
        const links=await page.locator("table a[href^='"+base(site)+"/edit/']").evaluateAll(elements=>elements.map(e=>e.getAttribute("href")));
        check(links.length>0&&links.length<=50,"content page size="+links.length+" exceeded bounded range1..50");
        for(const link of links) {check(!ids.has(link),"content pagination repeated an item");ids.add(link);}
        const next=page.locator("a[href^='"+base(site)+"/page/']");
        if(await next.count()===0) break;
        check(pages<4,"content pagination did not terminate");
        const [navigation]=await Promise.all([page.waitForNavigation({waitUntil:"domcontentloaded"}),next.click()]);
        check(navigation?.status()===200,"content continuation HTTP status="+navigation?.status());
        await page.locator("table").waitFor({state:"attached"});
      }
      check(ids.size===site.content_count&&ids.size>100&&pages===3,"content discovery incomplete beyond100");
      await page.goto(fixture.control+base(site)+"/edit/"+site.content_id);
      await scope(site);
      const revisions=new Set();let historyPages=0;
      for(;;) {
        historyPages++;
        const links=await page.locator("ol a[href^='"+base(site)+"/preview/"+site.content_id+"/']").evaluateAll(elements=>elements.map(e=>e.getAttribute("href")));
        check(links.length>0&&links.length<=25,"history page size="+links.length+" exceeded bounded range1..25");
        links.forEach(link=>{check(!revisions.has(link),"history pagination repeated revision");revisions.add(link);});
        const next=page.locator("a[href^='"+base(site)+"/history/"+site.content_id+"/before/']");
        if(await next.count()===0) break;
        check(historyPages<4,"history pagination did not terminate");
        const [navigation]=await Promise.all([page.waitForNavigation({waitUntil:"domcontentloaded"}),next.click()]);
        check(navigation?.status()===200,"history continuation HTTP status="+navigation?.status());
        await page.locator("ol").waitFor({state:"attached"});
      }
      check(revisions.size===site.history_count&&revisions.size>50&&historyPages===3,"history discovery incomplete beyond50");
      await page.goto(fixture.control+base(site)+"/edit/"+site.content_id);
      const source=await page.locator("[data-editor-source]").inputValue();
      check(source.includes('href="/relative-proof/"'),"canonical stored relative source changed");
      const preview=await context.newPage();
      try {
        await preview.goto(await page.locator("[data-preview-link]").getAttribute("href").then(path=>fixture.control+path));
        check(await preview.getByRole("link",{name:"Scoped relative link",exact:true}).getAttribute("href")===site.origin+"/relative-proof/","preview relative link resolved against control instead of selected site");
        check(await preview.locator("base").count()===0,"preview added a document base");
      } finally {await preview.close();}
      await responsive(site.id+"-editor");
      await page.locator("[data-content-save] input[name=title]").fill("Control saved "+site.id+" فارسی <img id=fixture-user-markup>");
      await submitSave();
      check(await page.locator("#fixture-user-markup").count()===0,"saved title became markup");
      mark(site.id+"-pagination-history-preview-save");
    }
    const site=fixture.sites[0],other=fixture.sites[1];
    await page.goto(fixture.control+base(site)+"/");
    await page.locator("[data-content-create] input[name=title]").fill("Control-only created فارسی");
    await Promise.all([page.waitForURL(url=>url.pathname.startsWith(base(site)+"/edit/")),page.locator("[data-content-create] button[type=submit]").click()]);
    check(await page.locator("[data-current-revision]").textContent()==="1","control create did not reach scoped edit");
    await scope(site);mark("control-create-open");

    await page.goto(fixture.control+base(site)+"/edit/"+site.content_id);
    const staleHead=await page.locator("[data-current-revision]").textContent();
    const localTitle="Unsent conflict text فارسی";
    await page.locator("[data-content-save] input[name=title]").fill(localTitle);
    await cancelledNavigation();
    const race=await page.evaluate(()=>crypto.randomUUID());
    await apiMutation(site,base(site)+"/save",{operation_id:race,id:site.content_id,expected_head:staleHead,title:"Concurrent committed source",body_html:"<p>Concurrent source.</p>"});
    await page.locator("[data-content-save] button[type=submit]").click();
    await page.locator("[data-conflict-recovery]:not([hidden])").waitFor();
    check(await page.locator("[data-content-save] input[name=title]").inputValue()===localTitle&&await page.locator("[data-content-save] input[name=expected_head]").inputValue()===staleHead,"stale save discarded local input/CAS");
    const [latest]=await Promise.all([context.waitForEvent("page"),page.locator("[data-latest-link]").click()]);
    await latest.waitForLoadState();
    check(await latest.locator("[data-content-save] input[name=title]").inputValue()==="Concurrent committed source","latest-source inspection wrong");
    check(await page.locator("[data-content-save] input[name=title]").inputValue()===localTitle,"opening latest changed original DOM");
    await latest.close();await discardAndGoto(fixture.control+base(site)+"/edit/"+site.content_id);
    mark("stale-cas-input-and-navigation-preservation");

    await page.locator("[data-content-save] input[name=title]").fill("Acknowledgement lost فارسی");
    const uncertain=await page.locator("[data-content-save] input[name=operation_id]").inputValue();
    const uncertainHead=await page.locator("[data-current-revision]").textContent();
    fault="commit_then_abort";
    await page.locator("[data-content-save] button[type=submit]").click();
    await page.locator("[data-draft-recovery]:not([hidden])").waitFor();
    check(await page.locator("[data-pending-operation]").textContent()===uncertain,"uncertain operation identity lost");
    check(await page.locator("[data-content-save] input[name=title]").isDisabled()&&await page.locator("[data-editor]").getAttribute("contenteditable")==="false","unknown save did not freeze payload");
    await cancelledNavigation();
    await page.locator("[data-inspect-pending]").click();
    await page.locator("[data-draft-recovery]").waitFor({state:"hidden"});
    check(await page.locator("[data-current-revision]").textContent()!==uncertainHead,"same-ID inspection did not reconcile committed save");
    check(await page.locator("[data-content-save] input[name=operation_id]").inputValue()!==uncertain,"confirmed result did not rotate operation");
    const changed={operation_id:uncertain,id:site.content_id,expected_head:uncertainHead,title:"changed payload must conflict",body_html:"<p>changed</p>"};
    await apiMutation(site,base(site)+"/save",changed,409,"negative");
    mark("committed-lost-ack-same-id-inspection-changed-payload-conflict");

    await page.locator("[data-content-save] input[name=title]").fill("Exact retry retained فارسی");
    const retryID=await page.locator("[data-content-save] input[name=operation_id]").inputValue();
    fault="never_sent";
    await page.locator("[data-content-save] button[type=submit]").click();
    await page.locator("[data-draft-recovery]:not([hidden])").waitFor();
    await page.locator("[data-inspect-pending]").click();
    await page.locator("#status").filter({hasText:await page.evaluate(()=>AChrixI18n.text("rixa","draft_inspection_missing"))}).waitFor();
    check(await page.locator("[data-draft-recovery]").isVisible()&&await page.locator("[data-content-save] input[name=title]").isDisabled(),"lookup404 falsely proved no commit");
    fault="retry_exact";
    await page.locator("[data-retry-pending]").click();
    await page.locator("[data-draft-recovery]").waitFor({state:"hidden"});
    check(await page.locator("[data-content-save] input[name=operation_id]").inputValue()!==retryID,"same-ID exact retry did not resolve");
    check(faults===2&&routeErrors.length===0,"transport proof incomplete or journal failed");
    mark("missing-inspection-keeps-pending-explicit-byte-identical-retry");

    const retainedTitle="Session-expired DOM text فارسی";
    await page.locator("[data-content-save] input[name=title]").fill(retainedTitle);
    const expectedHead=await page.locator("[data-content-save] input[name=expected_head]").inputValue();
    const logout=await api.post(fixture.control+"/auth/logout",{headers:{"Origin":fixture.control,"X-Identity-Request":"1","X-CSRF-Token":await csrf()}});
    check(logout.status()===204,"could not revoke the fixture control session");
    await page.locator("[data-content-save] button[type=submit]").click();
    await page.locator("[data-session-recovery]:not([hidden])").waitFor();
    check(await page.locator("[data-content-save] input[name=title]").inputValue()===retainedTitle&&await page.locator("[data-content-save] input[name=expected_head]").inputValue()===expectedHead,"expired session discarded DOM/CAS");
    const [signin]=await Promise.all([context.waitForEvent("page"),page.locator("[data-session-recovery] a").click()]);
    await login(signin);await signin.close();
    check(await page.locator("[data-content-save] input[name=title]").inputValue()===retainedTitle,"new-tab login replaced original DOM");
    await submitSave();mark("revoked-session-new-tab-recovery-preserves-dom");

    for(const tag of ["fa","ar","en"]) {
      await locale(tag);await page.goto(fixture.control+base(site)+"/edit/"+site.content_id);await scope(site);
      check(await page.locator("html").getAttribute("lang")===tag,"selected workspace lost locale");
      check(await page.locator("[data-content-save] button[type=submit]").textContent()===await page.evaluate(()=>AChrixI18n.text("rixa","save_revision")),"visible selected editor label disagrees with shared catalog");
      if(tag==="fa") await responsive("site-a-fa");
      if(tag==="ar") await responsive("site-a-installed-ar");
    }
    const denied=await api.get(fixture.control+base(site)+"/edit/"+other.content_id,{maxRedirects:0});
    check(denied.status()===404,"foreign content ID escaped selected site");
    const forged=await api.get(fixture.control+"/admin/sites/site/not-declared/content/",{maxRedirects:0});
    check([403,404].includes(forged.status()),"unknown site selection was accepted");
    const token=await csrf();
    const badOriginPayload={operation_id:await page.evaluate(()=>crypto.randomUUID()),id:site.content_id,expected_head:await page.locator("[data-current-revision]").textContent(),title:"denied",body_html:"<p>denied</p>"};
    await record({type:"negative",site_id:site.id,path:base(site)+"/save",payload:JSON.stringify(badOriginPayload)});
    const wrongOrigin=await api.post(fixture.control+base(site)+"/save",{headers:{"Origin":fixture.site_a,"X-Identity-Request":"1","X-CSRF-Token":token},data:badOriginPayload});
    check([401,403].includes(wrongOrigin.status()),"foreign Origin reached selected mutation");
    const anonymous=await browser.newContext({ignoreHTTPSErrors:true});
    const anonymousApi={get:(url,options)=>fixtureRequest(url,{...options,method:"GET"},anonymous),post:(url,options)=>fixtureRequest(url,{...options,method:"POST"},anonymous)};
    try {
      const privateResponse=await anonymousApi.get(fixture.control+base(site)+"/preview/"+site.content_id+"/56",{maxRedirects:0});
      check([303,401,403].includes(privateResponse.status()),"anonymous selected preview not denied");
      check(!(await privateResponse.text()).includes("Private selected revision"),"anonymous selected preview leaked source");
      const direct=await api.get(fixture.site_a+"/admin/content/",{maxRedirects:0});
      check([303,401,403].includes(direct.status()),"control cookie became universal site access");
      const siteLogin=await anonymousApi.post(fixture.site_a+"/auth/login",{headers:{"Origin":fixture.site_a,"X-Identity-Request":"1"},data:{login:credentials.site_a[0],password:credentials.site_a[1]}});
      check(siteLogin.status()===200,"separate site login failed");
      const identity=(siteLogin.rawHeaders["set-cookie"]||[])[0]?.split(";")[0];
      check(identity&&identity.includes("="),"separate site login cookie missing");
      const foreignCookie=await anonymousApi.post(fixture.control+base(site)+"/save",{headers:{"Cookie":identity,"Origin":fixture.control,"X-Identity-Request":"1","X-CSRF-Token":(await siteLogin.json()).csrf},data:badOriginPayload});
      check([401,403].includes(foreignCookie.status()),"site cookie became control authority");
    } finally {await anonymous.close();}
    check(await page.evaluate(()=>localStorage.length===0&&sessionStorage.length===0),"draft/session data written to WebStorage");
    check(!page.url().includes("title=")&&!page.url().includes("body="),"draft text appeared in URL");
    check(errors.length===0&&routeErrors.length===0,"browser errors: "+errors.concat(routeErrors).join("; "));
    mark("scoped-privacy-origin-cookie-storage-negatives");
    await record({type:"complete",checks:result.checks});
    return result;
  } finally {
    await context.unroute(routePattern,handler);
    await queue;
    await journal.close();
  }
}
