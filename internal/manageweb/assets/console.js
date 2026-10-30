// SPDX-License-Identifier: MPL-2.0
"use strict";
(() => {
 const i18n=window.AChrixI18n;
 const t=key=>i18n.text("rixa-manager",key);
 const status=document.getElementById("status");
 const root=document.querySelector("[data-console]");
 const permit=document.querySelector("[data-permit]");
 let busy=false,dirty=false,pending=null,inspectedPending=false,reviewToken="",actions=[],pollTimer;
 function announce(key,failure=false){status.textContent=t(key);status.setAttribute("role",failure?"alert":"status");status.focus();}
 function mayLeave(){return !busy&&(!(dirty||pending)||window.confirm(t("leave_warning")));}
 document.querySelectorAll("[data-language]").forEach(select=>select.addEventListener("change",()=>{if(mayLeave())location.assign("/language/"+encodeURIComponent(select.value));else select.value=document.documentElement.lang;}));
 window.addEventListener("beforeunload",event=>{if(busy||dirty||pending){event.preventDefault();event.returnValue="";}});
 document.addEventListener("click",event=>{
  const link=event.target.closest("a[href]"),logout=event.target.closest("#logout");
  if(!logout&&(!link||link.target==="_blank"||event.ctrlKey||event.metaKey||event.shiftKey||event.altKey))return;
  if(!mayLeave()){event.preventDefault();event.stopImmediatePropagation();}
 },true);
 async function csrf(){
  const response=await fetch("/console-auth/csrf",{mode:"same-origin",redirect:"error",credentials:"same-origin",cache:"no-store",referrer:location.origin+"/",referrerPolicy:"same-origin",headers:{"X-Identity-Request":"1"}});
  if(!response.ok)throw {status:response.status,unknown:false};
  const result=await response.json();if(typeof result.csrf!=="string")throw {status:0,unknown:false};
  return result.csrf;
 }
 async function request(path,body,readOnly=false,login=false){
  const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),path.startsWith("/artifact/")?180000:15000);
  let sent=false;
  try{
   const headers={"X-Identity-Request":"1"};
   if(!readOnly&&!login)headers["X-CSRF-Token"]=await csrf();
   if(typeof body==="string")headers["Content-Type"]="application/json";
   sent=!readOnly;
   const response=await fetch(path,{method:readOnly?"GET":"POST",mode:"same-origin",redirect:"error",credentials:"same-origin",cache:"no-store",referrer:location.origin+"/",referrerPolicy:"same-origin",headers,body:readOnly?undefined:body,signal:controller.signal});
   let result;try{result=await response.json();}catch(_){}
   if(!response.ok)throw {status:response.status,result,unknown:sent&&![400,401,403,404,409,413,429].includes(response.status)};
   if(!result)throw {status:0,unknown:sent};
   return result;
  }catch(error){throw {status:error.status||0,result:error.result,unknown:error.unknown===undefined?sent:error.unknown};}
  finally{clearTimeout(timer);}
 }
 if(permit){
  permit.addEventListener("input",()=>{dirty=true;});
  permit.addEventListener("submit",async event=>{
   event.preventDefault();if(busy||!permit.reportValidity())return;
   busy=true;permit.querySelector("button").disabled=true;announce("working");
   try{const result=await request(permit.action,JSON.stringify({permit:permit.elements.permit.value}),false,true);if(result.location!=="/admin/operation/")throw {};dirty=false;location.assign(result.location);}
   catch(_){try{await csrf();dirty=false;location.assign("/admin/operation/");}catch(_){announce("access_required",true);}}
   finally{busy=false;permit.querySelector("button").disabled=false;}
  });
  return;
 }
 if(!root)return;
 const configure=root.querySelector("[data-configure]"),pendingBox=root.querySelector("[data-pending]");
 function lock(){
  const held=busy||!!pending;
  root.querySelector("[data-config]").hidden=!actions.includes("configure");
  root.querySelector("[data-remove]").hidden=!actions.includes("remove");
  root.querySelectorAll("form input,form select,form button").forEach(control=>{control.disabled=held;});
  root.querySelectorAll("[data-action]").forEach(button=>{button.hidden=!actions.includes(button.dataset.action);button.disabled=held||dirty;});
  root.querySelector('[data-action-form="apply"]').hidden=!actions.includes("apply");
  root.querySelector('[data-action-form="apply"] button').disabled=held||dirty||!reviewToken;
  root.querySelector("[data-inspect]").disabled=busy;
  pendingBox.hidden=!pending;
  const retry=root.querySelector("[data-retry]");retry.hidden=!pending||!inspectedPending||!actions.includes(pending.action==="artifact"?"configure":pending.action);retry.disabled=busy;
 }
 function matches(result){return result&&result.operation_id===root.dataset.operation&&result.kind===root.dataset.kind&&typeof result.state==="string";}
 function display(result){
  if(!matches(result))throw {unknown:true};
  clearTimeout(pollTimer);
  actions=Array.isArray(result.actions)?result.actions:[];
  root.querySelector("[data-state]").textContent=result.message||t("unavailable");
  root.querySelector("[data-code]").textContent=[result.native_operation_id,result.state,result.stage,result.code].filter(Boolean).join(" · ");
  const problems=root.querySelector("[data-problems]");problems.replaceChildren();
  configure.querySelectorAll('[aria-invalid="true"]').forEach(input=>{input.removeAttribute("aria-invalid");input.removeAttribute("aria-describedby");});
  for(const problem of result.problems||[]){
   const item=document.createElement("li"),id=document.createElement("bdi");id.dir="ltr";id.textContent=problem.field+" · "+problem.code;
   item.id="problem-"+problems.children.length;item.append(id,document.createTextNode(" — "+problem.message));problems.append(item);
   const input=configure.elements.namedItem(problem.field);if(input){input.setAttribute("aria-invalid","true");input.setAttribute("aria-describedby",item.id);}
  }
  if(result.artifact)root.querySelector("[data-artifact]").textContent=[result.artifact.release,result.artifact.source_commit,result.artifact.sha256].filter(Boolean).join(" · ");
  if(result.state!=="ready")reviewToken="";
  if(result.review){const summary=root.querySelector("[data-review]");summary.replaceChildren();summary.hidden=false;for(const [name,value] of Object.entries(result.review)){const label=document.createElement("dt"),detail=document.createElement("dd"),id=document.createElement("bdi");label.textContent=t(name);id.dir="ltr";id.textContent=value;detail.append(id);summary.append(label,detail);}}
  if(result.state==="applying"&&!pending&&!result.access_closed)pollTimer=setTimeout(()=>inspect(false),2000);
  const admin=root.querySelector("[data-admin-url]");admin.hidden=true;
  if(result.admin_url){try{const url=new URL(result.admin_url);if(url.protocol==="https:"&&!url.username&&!url.password){admin.href=url.href;admin.hidden=false;}}catch(_){}}
  if(result.access_closed){actions=[];pending=null;dirty=false;reviewToken="";announce("access_complete");}
  lock();
 }
 async function inspect(explicit=true){
  if(busy)return;
  busy=true;lock();
  try{
   const result=await request("/admin/operation/status",undefined,true);
   if(!matches(result))throw {};
   inspectedPending=!!pending;
   if(pending&&(["applying","reconciliation-required","installed","removed","rejected"].includes(result.state))){pending=null;}
   if(pending?.action==="review"&&result.state==="ready"){pending=null;reviewToken="";}
   display(result);
   if(explicit)announce(pending?"uncertain_help":"inspected");
  }catch(_){if(explicit)announce("inspection_failed",true);}
  finally{busy=false;lock();}
 }
 async function send(attempt){
  if(busy)return;
  busy=true;lock();announce("working");
  try{
   const result=await request(attempt.path,attempt.body);
   if(attempt.action!=="artifact"&&!matches(result))throw {unknown:true};
   pending=null;
   if(attempt.action==="artifact"){
    root.querySelector("[data-artifact]").textContent=[result.artifact.release,result.artifact.source_commit,result.artifact.sha256].join(" · ");announce("artifact_ready");
   }else{
    display(result);
    if(attempt.action==="configure"){
     dirty=false;reviewToken="";
     configure.querySelectorAll('input[type="password"]').forEach(input=>{input.value="";input.required=false;input.placeholder=t("saved_secret");});
    }
    if(attempt.action==="review"&&!result.access_closed&&typeof result.review_token==="string")reviewToken=result.review_token;
    announce(result.access_closed?"access_complete":"updated");
   }
  }catch(error){
   if(error.result?.operation_id){try{display(error.result);}catch(_){}}
   if(error.unknown){pending=attempt;inspectedPending=false;announce("uncertain_help",true);}
   else announce(error.status===401?"access_expired":error.status===409?"action_unavailable":"correct_input",true);
  }finally{busy=false;lock();}
 }
 configure.addEventListener("input",()=>{dirty=true;reviewToken="";lock();});
 configure.addEventListener("submit",event=>{
  event.preventDefault();if(busy||pending||!configure.reportValidity())return;
  const value={};for(const control of configure.elements){if(control.name)value[control.name]=control.value;}
  reviewToken="";send(Object.freeze({action:"configure",path:configure.action,body:JSON.stringify(value)}));
 });
 root.querySelectorAll("[data-action]").forEach(button=>button.addEventListener("click",()=>{if(!busy&&!pending&&!dirty)send(Object.freeze({action:button.dataset.action,path:"/admin/operation/"+button.dataset.action,body:"{}"}));}));
 root.querySelectorAll("[data-action-form]").forEach(form=>form.addEventListener("submit",event=>{
  event.preventDefault();if(busy||pending||dirty||!form.reportValidity())return;
  const action=form.dataset.actionForm,body=action==="apply"?JSON.stringify({review_token:reviewToken}):JSON.stringify({confirmation:form.elements.confirmation.value});
  send(Object.freeze({action,path:form.action,body}));
 }));
 root.querySelector("[data-fetch]").addEventListener("submit",event=>{
  event.preventDefault();const form=event.target;if(busy||pending||!form.reportValidity())return;
  const release=form.elements.release.value;const value=release?{release}:{channel:form.elements.channel.value};
  send(Object.freeze({action:"artifact",path:form.action,body:JSON.stringify(value)}));
 });
 root.querySelector("[data-upload]").addEventListener("submit",event=>{
  event.preventDefault();const form=event.target;if(busy||pending||!form.reportValidity())return;
  const body=new FormData();for(const name of ["release","source_commit"])body.append(name,form.elements[name].value);
  for(const name of ["archive","bundle"])body.append(name,form.elements[name].files[0]);
  send(Object.freeze({action:"artifact",path:form.action,body}));
 });
 root.querySelector("[data-inspect]").addEventListener("click",()=>inspect(true));
 root.querySelector("[data-retry]").addEventListener("click",()=>{if(pending&&!busy)send(pending);});
 lock();inspect(false);
})();
