// SPDX-License-Identifier: MPL-2.0
"use strict";
const fs=require("node:fs/promises");
const path=require("node:path");
const {chromium}=require("playwright");
const check=(ok,why)=>{if(!ok)throw new Error(why)};

async function main(){
  const root=process.argv[2];
  check(root&&path.isAbsolute(root)&&root!=="/"&&path.normalize(root)===root,"absolute private test root required");
  const info=await fs.lstat(root);
  check(info.isDirectory()&&!info.isSymbolicLink()&&(info.mode&0o077)===0,"test root not private");
  const f=JSON.parse(await fs.readFile(path.join(root,"ready.json"),"utf8"));
  check(f.schema==="rixa.code-theme-browser.v1","unexpected Rixa Theme fixture");
  for(const [k,h] of Object.entries({
    control:"control.localhost",management_a:"manage-a.localhost",management_b:"manage-b.localhost",
    public_a:"a.localhost",public_b:"b.localhost",preview_a:"preview-a.localhost",preview_b:"preview-b.localhost"
  })){const u=new URL(f[k]);check(u.protocol==="https:"&&u.hostname===h&&u.port&&u.pathname==="/"&&u.search==="","unapproved host "+k)}
  const errors=[],passes=[];
  const browser=await chromium.launch({headless:true});
  const context=await browser.newContext({ignoreHTTPSErrors:true,acceptDownloads:true,viewport:{width:1366,height:900}});
  const page=await context.newPage();
  page.setDefaultTimeout(20000);
  page.on("pageerror",e=>errors.push(e.message));
  const visit=async (p,url)=>{const r=await p.goto(url,{waitUntil:"networkidle"});check(r&&r.status()===200,"unexpected response "+url+" "+r?.status());return r};
  const waitRevision=async n=>page.waitForFunction(n=>document.querySelector("[data-theme-current]")?.textContent.trim()===n,String(n));
  const waitGeneration=async prev=>{
    await page.waitForFunction(prev=>{
      const value=document.querySelector("[data-theme-generation]")?.textContent.trim();
      return value&&value!==prev&&/^[A-Z2-7]{26}$/.test(value);
    },prev);
    return (await page.locator("[data-theme-generation]").textContent()).trim();
  };
  const newPublic=async url=>{
    const p=await context.newPage();
    p.on("pageerror",e=>errors.push(e.message));
    const r=await visit(p,url);
    return {p,r};
  };
  try{
    const baseline=await newPublic(f.public_a);
    check(await baseline.p.locator("html").getAttribute("lang")==="fa"&&
      await baseline.p.locator("html").getAttribute("dir")==="rtl","public baseline not fa/rtl");
    await baseline.p.close();

    await visit(page,f.management_a+"/admin/login");
    await page.locator("form[data-login] input[name=login]").fill("site-a-admin");
    await page.locator("form[data-login] input[name=password]").fill("A-Strong-Site-A-Password-2026!");
    await Promise.all([
      page.waitForURL(u=>u.origin===f.management_a&&u.pathname!=="/admin/login"),
      page.locator("form[data-login] button[type=submit]").click()
    ]);
    await page.locator("#logout").waitFor();
    check((await context.cookies(f.management_a)).some(c=>c.name==="__Host-AChrix-Session"&&!c.domain.startsWith("."))&&
      (await context.cookies(f.public_a)).length===0&&(await context.cookies(f.preview_a)).length===0,
      "Management Admin session leaked to public/preview");
    passes.push("real-management-https-host-only-session");

    await visit(page,f.management_a+"/admin/site-theme/");
    await page.locator("[data-theme-save-button]:not([disabled])").waitFor();
    check((await page.locator("[data-theme-current]").textContent()).trim()==="0","initial Theme is not built-in");
    const body=await page.locator("#theme-body").inputValue();
    check(body.includes(".Content.BodyHTML")&&body.includes(".Home.Items"),"default Theme binding missing");
    const tailored=body.replace("{{if .Content}}",
      '{{if .Content}}{{if eq .Content.Kind "page"}}<aside data-page-only>Page-only branch</aside>{{end}}');
    check(tailored!==body,"fixture Page branch could not be installed");
    for(const field of ["#theme-header","#theme-css","#theme-js"]){
      await page.locator("details:has("+field+") > summary").click();
    }
    await page.locator("#theme-header").fill('<header class="custom"><strong>Theme v1 فارسی</strong></header>');
    await page.locator("#theme-css").fill('body{margin:0;background:#fafafa}.custom{padding:1rem}main{margin-inline:1rem}');
    await page.locator("#theme-js").fill('document.body.dataset.demo="theme-v1";');
    await page.locator("#theme-body").fill(tailored);
    await page.locator("[data-theme-save] button[type=submit]").click();
    await waitRevision(1);
    const bundle=await page.evaluate(async()=>{
      const r=await fetch("/admin/site-theme/bundle/1",{credentials:"same-origin",cache:"no-store"});
      return {status:r.status,json:await r.json()};
    });
    check(bundle.status===200&&bundle.json.header_html.includes("Theme v1 فارسی")&&
      bundle.json.body_html===tailored,"saved immutable Theme not loadable");
    await page.locator("[data-theme-compare-revision]").fill("0");
    await page.locator("[data-theme-compare-trigger]").click();
    await page.locator("[data-theme-diff-output]:not([hidden])").waitFor();
    check((await page.locator("[data-theme-diff-output]").textContent()).includes("Theme v1"),
      "Theme source diff against built-in revision 0 unavailable");
    const zeroBundle=await page.evaluate(async()=>{
      const response=await fetch("/admin/site-theme/bundle/0",{credentials:"same-origin"});
      return {status:response.status,source:await response.json()};
    });
    check(zeroBundle.status===200&&zeroBundle.source.header_html.includes("Site.Title")&&
      !zeroBundle.source.header_html.includes("Theme v1"),"built-in revision 0 became inaccessible");
    const before=await newPublic(f.public_a);
    check(await before.p.locator("body").getAttribute("data-demo")===null,"Theme draft changed public output");
    await before.p.close();
    passes.push("source-hydration-save-draft-without-publish");

    // Exercise actual Theme POST admission with a syntactically valid draft.
    // Rejected requests must not create a new source revision.
    const writePayload=()=>({
      operation_id:require("node:crypto").randomUUID(),
      expected_head:"1",
      head_html:bundle.json.head_html,header_html:bundle.json.header_html,
      body_html:bundle.json.body_html,footer_html:bundle.json.footer_html,
      site_css:bundle.json.css,site_js:bundle.json.javascript,
      settings_json:JSON.stringify(bundle.json.settings||{})
    });
    const noToken=await context.request.post(f.management_a+"/admin/site-theme/save",{
      headers:{"Content-Type":"application/json","Origin":f.management_a,
        "X-Identity-Request":"1"},data:JSON.stringify(writePayload()),maxRedirects:0
    });
    check(noToken.status()===401||noToken.status()===403,
      "Theme write without CSRF was not rejected: "+noToken.status());
    const invalidToken=await context.request.post(f.management_a+"/admin/site-theme/save",{
      headers:{"Content-Type":"application/json","Origin":f.management_a,
        "X-Identity-Request":"1","X-CSRF-Token":"invalid"},data:JSON.stringify(writePayload()),maxRedirects:0
    });
    check(invalidToken.status()===401||invalidToken.status()===403,
      "Theme write with invalid CSRF was not rejected: "+invalidToken.status());
    check((await page.locator("[data-theme-current]").textContent()).trim()==="1",
      "invalid Theme POST modified the revision");
    const validToken=await page.evaluate(async()=>{
      const response=await fetch("/auth/csrf",{
        credentials:"same-origin",headers:{"X-Identity-Request":"1"},
        referrer:location.origin+"/",referrerPolicy:"same-origin",cache:"no-store"
      });
      return response.ok?(await response.json()).csrf:"";
    });
    check(validToken.length>20,"active Site Admin did not provide normal CSRF token");
    const untrustedOrigin=await context.request.post(f.management_a+"/admin/site-theme/save",{
      headers:{"Content-Type":"application/json","X-Identity-Request":"1",
        "Origin":"https://outside.localhost","Referer":f.management_a+"/",
        "X-CSRF-Token":validToken},
      data:JSON.stringify(writePayload()),maxRedirects:0
    });
    check(untrustedOrigin.status()>=400&&untrustedOrigin.status()<500,
      "Theme write with untrusted Origin was not rejected: "+untrustedOrigin.status());
    passes.push("theme-save-post-csrf-and-origin-denial");

    await page.locator("[data-theme-preview] button[type=submit]").click();
    await page.locator("[data-theme-preview-link]:not([hidden])").waitFor();
    const previewURL=await page.locator("[data-theme-preview-link]").getAttribute("href");
    check(previewURL?.startsWith(f.preview_a+"/entry/"),"preview origin not isolated");
    const preview=await newPublic(previewURL);
    check(await preview.p.locator("header.custom").count()===1&&
      await preview.p.locator("body").getAttribute("data-demo")==="theme-v1"&&
      await preview.p.locator("html").getAttribute("dir")==="rtl","Preview HTML/CSS/JS/RTL mismatch");
    check(preview.r.headers()["cache-control"]?.includes("no-store")&&
      preview.r.headers()["content-security-policy"]?.includes("script-src 'self'")&&
      !preview.r.headers()["content-security-policy"]?.includes("script-src 'unsafe-inline'"),
      "Preview CSP/cache boundary failed");
    check((await preview.p.goto(f.preview_a+"/admin")).status()===404,"Preview exposed Admin");
    await preview.p.close();
    check((await context.cookies(f.preview_a)).some(c=>c.name==="rixa-theme-preview"&&c.httpOnly&&c.secure)&&
      !(await context.cookies(f.preview_a)).some(c=>c.name==="__Host-AChrix-Session"),
      "Preview shared privileged cookies");
    passes.push("isolated-ephemeral-preview-and-csp");

    let generation=f.generation_a;
    check((await page.locator("[data-theme-generation-field]").inputValue())===generation,
      "Preview changed active publication");
    await page.locator("[data-theme-publish] input[name=confirm]").check();
    await page.locator("[data-theme-publish] button[type=submit]").click();
    generation=await waitGeneration(generation);
    const published=await newPublic(f.public_a);
    check(await published.p.locator("header.custom").count()===1&&
      await published.p.locator("body").getAttribute("data-demo")==="theme-v1","public generation did not activate JS");
    check(published.r.headers()["content-security-policy"]?.includes("script-src 'self'")&&
      !published.r.headers()["content-security-policy"]?.includes("script-src 'unsafe-inline'"),
      "published static page CSP relaxed");
    for (const [route, schemaKind, expectedBody] of [
      [f.post_path,"BlogPosting","Visible HTML body"],
      [f.page_path,"WebPage","Page-specific published body"]
    ]) {
      const result=await newPublic(f.public_a+route);
      const canonical=await result.p.locator('link[rel="canonical"]').getAttribute("href");
      const structured=await result.p.locator('script[type="application/ld+json"]').textContent();
      const content=await result.p.locator("body").textContent();
      check(canonical===f.public_a+route&&structured.includes(schemaKind)&&
        content.includes(expectedBody)&&await result.p.locator("html").getAttribute("dir")==="rtl",
        "Published Theme route, SEO or RTL mismatch: "+schemaKind);
      const pageOnly=await result.p.locator("[data-page-only]").count();
      check(pageOnly===(schemaKind==="WebPage"?1:0),
        "Page-specific template branch wrong for "+schemaKind);
      await result.p.close();
    }
    passes.push("home-post-page-canonical-structured-data-rtl");
    await published.p.screenshot({path:path.join(root,"code-theme-desktop.png"),fullPage:true});
    await published.p.setViewportSize({width:390,height:844});
    await published.p.screenshot({path:path.join(root,"code-theme-mobile.png"),fullPage:true});
    check(await published.p.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+2),
      "mobile Theme overflowed viewport");
    check((await published.p.goto(f.public_a+"/admin")).status()===404,"public site exposes Admin");
    await published.p.close();
    check((await context.cookies(f.public_a)).length===0,"public origin acquired Admin session");
    passes.push("explicit-publish-browser-executable-responsive-csp");

    const stale=await page.evaluate(async previous=>{
      const tokenResponse=await fetch("/auth/csrf",{
        mode:"same-origin",redirect:"error",referrer:location.origin+"/",
        referrerPolicy:"same-origin",headers:{"X-Identity-Request":"1"},
        credentials:"same-origin",cache:"no-store"
      });
      if(!tokenResponse.ok)return tokenResponse.status;
      const token=await tokenResponse.json();
      const result=await fetch("/admin/site-theme/publish",{
        method:"POST",credentials:"same-origin",
        headers:{"Content-Type":"application/json","X-CSRF-Token":token.csrf,"X-Identity-Request":"1"},
        body:JSON.stringify({operation_id:crypto.randomUUID(),theme_revision:"1",expected_generation:previous,confirm:true})
      });
      return result.status;
    },f.generation_a);
    check(stale===409,"stale publication generation not rejected: "+stale);
    passes.push("concurrent-generation-cas");

    const [download]=await Promise.all([
      page.waitForEvent("download"),
      page.locator("a[href='/admin/site-theme/export/1']").click()
    ]);
    check(download.suggestedFilename()==="rixa-code-theme-v1.zip","incorrect portable download");
    const zip=await fs.readFile(await download.path());
    check(zip.length>200&&zip.readUInt32LE(0)===0x04034b50,"invalid ZIP format");
    await page.locator("[data-theme-import] input[type=file]").setInputFiles({
      name:"rixa-code-theme-v1.zip",mimeType:"application/zip",buffer:zip
    });
    await page.locator("[data-theme-import] button[type=submit]").click();
    await waitRevision(2);
    await page.locator("[data-theme-save-button]:not([disabled])").waitFor();
    check((await page.locator("#theme-header").inputValue()).includes("Theme v1"),"ZIP import changed source");
    passes.push("real-zip-export-import-private-draft");

    await page.locator("#theme-header").fill("<header>Theme v3</header>");
    await page.locator("#theme-js").fill('document.body.dataset.demo="theme-v3";');
    await page.locator("[data-theme-save] button[type=submit]").click();
    await waitRevision(3);
    await page.locator("[data-theme-publish] input[name=confirm]").check();
    await page.locator("[data-theme-publish] button[type=submit]").click();
    const previousGeneration=generation;
    generation=await waitGeneration(previousGeneration);
    const v3=await newPublic(f.public_a);
    check(await v3.p.locator("body").getAttribute("data-demo")==="theme-v3","Theme revision3 not activated");
    await v3.p.close();
    await page.locator("[data-theme-publish] [name=theme_revision]").fill("1");
    await page.locator("[data-theme-publish] input[name=confirm]").check();
    await page.locator("[data-theme-publish] button[type=submit]").click();
    generation=await waitGeneration(generation);
    const rollback=await newPublic(f.public_a);
    check(await rollback.p.locator("body").getAttribute("data-demo")==="theme-v1"&&
      await rollback.p.locator("header.custom").count()===1,"previous revision republish failed");
    await rollback.p.close();
    passes.push("older-revision-republished-as-new-generation");

    const siteB=await browser.newContext({ignoreHTTPSErrors:true});
    try {
      const b=await siteB.newPage();
      await visit(b,f.management_b+"/admin/login");
      await b.locator("form[data-login] input[name=login]").fill("site-b-admin");
      await b.locator("form[data-login] input[name=password]").fill("A-Strong-Site-B-Password-2026!");
      await Promise.all([
        b.waitForURL(u=>u.origin===f.management_b&&u.pathname!=="/admin/login"),
        b.locator("form[data-login] button[type=submit]").click()
      ]);
      await visit(b,f.management_b+"/admin/site-theme/");
      await b.locator("[data-theme-save-button]:not([disabled])").waitFor();
      check((await b.locator("[data-theme-current]").textContent()).trim()==="0"&&
        !await b.locator("#theme-header").inputValue().then(v=>v.includes("Theme v1")),
        "site A Theme source leaked to site B");
      await visit(b,f.public_b);
      check(!(await b.content()).includes("Theme v1")&&
        await b.locator("html").getAttribute("lang")==="en"&&
        await b.locator("html").getAttribute("dir")==="ltr",
        "site A publication leaked to B or English/LTR failed");
      check((await b.goto(f.preview_b+"/")).status()===404,"site B preview exposed without ticket");
    }finally{await siteB.close()}
    passes.push("two-postgres-sites-and-three-origins-isolated");

    check(errors.length===0,"Chromium JS errors: "+errors.join("; "));
    await fs.writeFile(path.join(root,"browser-evidence.json"),JSON.stringify({
      schema:f.schema,passes,active_generation:generation,errors
    },null,2),{flag:"wx",mode:0o600});
    console.log("RIXA_THEME_CHROMIUM_PASS "+passes.join(" "));
  }finally {
    await context.close();
    await browser.close();
  }
}
main().catch(error=>{console.error("RIXA_THEME_CHROMIUM_FAIL",error.stack||error);process.exitCode=1});
