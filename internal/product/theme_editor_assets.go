// SPDX-License-Identifier: MPL-2.0

package product

const themeEditorJS = `
"use strict";
(() => {
  const root = document.querySelector("[data-theme-workspace]");
  if (!root) return;
  const save = root.querySelector("[data-theme-save]");
  const importForm = root.querySelector("[data-theme-import]");
  const preview = root.querySelector("[data-theme-preview]");
  const publish = root.querySelector("[data-theme-publish]");
  const openPreview = root.querySelector("[data-theme-preview-link]");
  const loading = root.querySelector("[data-theme-loading]");
  const saveButton = root.querySelector("[data-theme-save-button]");
  const lookup = root.querySelector("[data-theme-operation-input]");
  const sourceNames = [
    ["head_html","head_html"],["header_html","header_html"],
    ["body_html","body_html"],["footer_html","footer_html"],
    ["site_css","css"],["site_js","javascript"]
  ];
  function uuid(){
    if (crypto.randomUUID) return crypto.randomUUID();
    const b = crypto.getRandomValues(new Uint8Array(16));
    b[6]=(b[6]&15)|64;b[8]=(b[8]&63)|128;
    const h=Array.from(b,v=>v.toString(16).padStart(2,"0")).join("");
    return h.slice(0,8)+"-"+h.slice(8,12)+"-"+h.slice(12,16)+"-"+h.slice(16,20)+"-"+h.slice(20);
  }
  function rotate(form){
    const operation=form?.querySelector("[data-operation-field]");
    if (operation) operation.value=uuid();
  }
  function output(message){
    if (loading) loading.textContent=message;
  }
  function setRevision(revision){
    root.querySelector("[data-theme-current]").textContent=revision;
    root.querySelectorAll("[data-theme-expected]").forEach(node=>node.value=revision);
    root.querySelectorAll("[data-theme-revision]").forEach(node=>node.value=revision);
    const exportCurrent=root.querySelector("[data-theme-current-export]");
    if (exportCurrent) exportCurrent.href="/admin/site-theme/export/"+revision;
  }
  async function fetchBundle(revision){
    const response=await fetch("/admin/site-theme/bundle/"+revision,{
      credentials:"same-origin",mode:"same-origin",redirect:"error",cache:"no-store"
    });
    if (!response.ok) throw new Error("Cannot load authorized Theme revision "+revision);
    return response.json();
  }
  let currentAssetInventory={};
  async function loadRevision(revision){
    saveButton.disabled=true;
    output("Loading immutable source revision "+revision+"...");
    const b=await fetchBundle(revision);
    currentAssetInventory=b.assets||{};
    for (const [name,field] of sourceNames) {
      const control=save.elements.namedItem(name);
      if (control) control.value=b[field] || "";
    }
    save.elements.namedItem("settings_json").value=JSON.stringify(b.settings||{},null,2);
    saveButton.disabled=false;
    output("Source revision "+revision+" loaded. Draft changes remain private until explicit Publish.");
  }
  const initial=root.querySelector("[data-theme-expected]").value;
  loadRevision(initial).catch(error=>output(error.message+"; save disabled to protect existing source."));

  // Bounded, text-only comparison. Source never enters innerHTML or executes
  // on the authenticated control origin; common-prefix/suffix avoids costly
  // quadratic diff engines and caps output for large source slots.
  const diffTrigger=root.querySelector("[data-theme-compare-trigger]");
  const diffResult=root.querySelector("[data-theme-diff-output]");
  diffTrigger?.addEventListener("click",async()=>{
    const revision=root.querySelector("[data-theme-compare-revision]").value.trim();
    if (!/^[0-9]+$/.test(revision) || revision.length>18){
      output("Enter a valid nonnegative Theme revision.");
      return;
    }
    diffTrigger.disabled=true;
    try{
      const older=await fetchBundle(revision);
      const selections=sourceNames.concat([["settings_json","settings"]]);
      const pieces=[];
      for (const [name,field] of selections){
        const control=save.elements.namedItem(name);
        const left=field==="settings"?JSON.stringify(older.settings||{},null,2):String(older[field]||"");
        const right=String(control?.value||"");
        if (left===right) continue;
        const previous=left.split(/\r\n|\n|\r/);
        const current=right.split(/\r\n|\n|\r/);
        let start=0;
        while(start<previous.length&&start<current.length&&previous[start]===current[start])start++;
        let oldEnd=previous.length,newEnd=current.length;
        while(oldEnd>start&&newEnd>start&&previous[oldEnd-1]===current[newEnd-1]){
          oldEnd--;newEnd--;
        }
        const removed=previous.slice(start,Math.min(start+40,oldEnd)).map(line=>"- "+line);
        const added=current.slice(start,Math.min(start+40,newEnd)).map(line=>"+ "+line);
        if(oldEnd-start>40) removed.push("- ... truncated");
        if(newEnd-start>40) added.push("+ ... truncated");
        pieces.push("=== "+name+" ===\n@@ starting line "+(start+1)+" @@\n"+
          removed.concat(added).join("\n").slice(0,15000));
      }
      const compareAssets=older.assets||{};
      const changedAssets=Array.from(new Set([
        ...Object.keys(compareAssets),...Object.keys(currentAssetInventory)
      ])).sort().filter(name=>{
        const before=compareAssets[name],now=currentAssetInventory[name];
        return !before||!now||before.mime!==now.mime||before.data!==now.data;
      });
      if(changedAssets.length){
        pieces.push("=== public asset inventory ===\n"+changedAssets.map(name=>"~ "+name).join("\n"));
      }
      diffResult.textContent=pieces.length?pieces.join("\n\n"):"No differences in the six source slots, public settings or included assets.";
      diffResult.hidden=false;
      output("Source comparison is read-only. Review changes before Save or Publish.");
    }catch(error){
      diffResult.textContent="Comparison unavailable: "+String(error.message||error);
      diffResult.hidden=false;
    }finally{diffTrigger.disabled=false}
  });

  // An import is prepared from a user-selected local file with no extra
  // privileged URL and no persistent browser storage. The shared Admin shell
  // still performs the authenticated CSRF-protected POST.
  let importReady=false;
  importForm?.addEventListener("submit",event=>{
    if(importReady) {importReady=false;return}
    event.preventDefault();
    event.stopImmediatePropagation();
    const file=importForm.querySelector("[data-theme-archive]")?.files?.[0];
    if(!file||file.size<1||file.size>4*1024*1024) {
      output("Select a nonempty Theme ZIP no larger than 4 MiB.");
      return;
    }
    file.arrayBuffer().then(buffer=>{
      const bytes=new Uint8Array(buffer);
      const slices=[];
      for(let i=0;i<bytes.length;i+=32768){
        slices.push(String.fromCharCode(...bytes.subarray(i,i+32768)));
      }
      importForm.elements.namedItem("archive_base64").value=btoa(slices.join(""));
      importReady=true;
      importForm.requestSubmit();
    }).catch(()=>output("The Theme ZIP could not be read."));
  },true);

  function afterSave(event){
    const revision=event.detail?.revision;
    if(!revision||!/^[0-9]+$/.test(revision))return;
    setRevision(revision);
    rotate(event.currentTarget);
    if(event.currentTarget===importForm){
      importForm.querySelector("[data-theme-archive]").value="";
      importForm.elements.namedItem("archive_base64").value="";
      loadRevision(revision).catch(error=>output(error.message));
    } else {
      output("Theme revision "+revision+" saved privately. Preview before publishing.");
    }
  }
  save?.addEventListener("admin:success",afterSave);
  importForm?.addEventListener("admin:success",afterSave);
  preview?.addEventListener("admin:success",event=>{
    const url=event.detail?.url;
    if(typeof url!=="string"||!url.startsWith("https://"))return;
    openPreview.href=url;
    openPreview.hidden=false;
    output("Isolated preview ready. Valid until "+(event.detail?.expires_at||"expiry")+".");
  });
  publish?.addEventListener("admin:success",event=>{
    const generation=event.detail?.generation;
    if(!generation||!/^[A-Z2-7]{26}$/.test(generation))return;
    const revision=event.detail?.theme_revision;
    root.querySelector("[data-theme-active]").textContent=revision;
    root.querySelector("[data-theme-generation]").textContent=generation;
    root.querySelector("[data-theme-generation-field]").value=generation;
    rotate(publish);
    output("Public generation "+generation+" committed. Verify the actual public Origin.");
  });
  for(const form of root.querySelectorAll("form[data-admin-form]")){
    form.addEventListener("admin:failure",event=>{
      if(!event.detail?.unknown)return;
      const id=form.elements.namedItem("operation_id")?.value;
      if(id&&lookup)lookup.value=id;
      output("Write acknowledgement unknown; inspect original operation "+(id||"")+" before any retry.");
    });
  }
  root.querySelector("[data-theme-operation]")?.addEventListener("admin:success",event=>{
    const d=event.detail||{};
    output("Committed operation: "+String(d.operation_id||"")+"; kind: "+String(d.kind||"")+
      "; revision: "+String(d.result_revision||d.theme_revision||"")+
      "; generation: "+String(d.generation||""));
  });
})();
`
