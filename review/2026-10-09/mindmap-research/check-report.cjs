const fs = require('node:fs');
const path = require('node:path');
const {fileURLToPath} = require('node:url');
const vm = require('node:vm');
const dir = __dirname;
const names = ['REPORT_RU.html','classic-audit.html','graph-audit.html','learning-research.html'];
const errors = [], checks = [];
const decode = s => s.replace(/&amp;/g,'&').replace(/&quot;/g,'"').replace(/&#39;/g,"'");
for(const name of names){
  const text=fs.readFileSync(path.join(dir,name),'utf8');
  const ids=[...text.matchAll(/\bid="([^"]+)"/g)].map(m=>decode(m[1]));
  const seen=new Set(ids);
  if(ids.length!==seen.size) errors.push(name+': duplicate HTML IDs');
  if(!text.includes('<html lang="ru">')) errors.push(name+': wrong language');
  if(/CATALOGUE_INSERT|<!-- AUDITS_AND_SCIENCE -->|<!-- ARCHITECTURE_AND_ROADMAP -->|<!-- SOURCES -->/.test(text)) errors.push(name+': unfinished placeholder');
  let local=0, external=0, anchors=0;
  for(const m of text.matchAll(/\bhref="([^"]+)"/g)){
    const href=decode(m[1]);
    if(/^https?:\/\//.test(href)){external++; continue;}
    if(href.startsWith('#')){anchors++; if(!seen.has(decodeURIComponent(href.slice(1)))) errors.push(name+': unknown anchor '+href); continue;}
    if(href==='verification.json'||href==='QA.json') continue;
    let target;
    try {target=href.startsWith('file:')?fileURLToPath(href):path.resolve(dir,decodeURIComponent(href.split('#')[0]));}
    catch(e){errors.push(name+': invalid link '+href);continue;}
    local++;
    if(!fs.existsSync(target)) errors.push(name+': missing file '+href);
  }
  checks.push({file:name,html_ids:ids.length,local_file_links:local,external_links:external,internal_anchors:anchors});
}
const features=JSON.parse(fs.readFileSync(path.join(dir,'features.json'),'utf8'));
const fmap=new Map(features.map(f=>[f.id,f]));
const done=new Set(), stack=new Set();
function visit(id){
  if(stack.has(id)){errors.push('Dependency cycle at '+id);return;}
  if(done.has(id))return;stack.add(id);
  for(const d of fmap.get(id).dependencies??[]) visit(d);
  stack.delete(id);done.add(id);
}
for(const f of features)visit(f.id);
const main=fs.readFileSync(path.join(dir,'REPORT_RU.html'),'utf8');
const cards=[...main.matchAll(/class="feature" id="/g)].length;
if(cards!==120)errors.push('Expected 120 HTML feature cards; got '+cards);
const scripts=[...main.matchAll(/<script>([\s\S]*?)<\/script>/g)].map(m=>m[1]);
for(const s of scripts){try{new Function(s);}catch(e){errors.push('Inline JS parse: '+e.message);}}
// Exercise the actual inline filter program with a minimal in-memory DOM model.
// This is not a browser, layout, accessibility or visual check.
const uiTests=[];
function assertUI(name,condition){uiTests.push({name,passed:!!condition});if(!condition)errors.push('Filter logic: '+name);}
try {
  const elements=new Map(), windowHandlers={};
  function element(id){const el={id,value:'',hidden:false,open:false,textContent:'',style:{},handlers:{},classList:{contains:()=>false},addEventListener(name,fn){this.handlers[name]=fn;},scrollIntoView(){this.scrolled=true;}}; elements.set(id,el);return el;}
  for(const id of ['f-search','f-category','f-state','f-milestone','f-priority','f-count','f-empty','f-reset','print-report'])element(id);
  const htmlCards=[...main.matchAll(/<details class="feature"\s+([^>]+)>/g)].map(m=>{
    const attrs=Object.fromEntries([...m[1].matchAll(/([\w-]+)="([^"]*)"/g)].map(x=>[x[1],decode(x[2]).replace(/&lt;/g,'<').replace(/&gt;/g,'>')]));
    const el=element(attrs.id);el.classList.contains=name=>name==='feature';
    el.dataset=Object.fromEntries(Object.entries(attrs).filter(([k])=>k.startsWith('data-')).map(([k,v])=>[k.slice(5),v]));return el;
  });
  const location={hash:''};
  const sandbox={document:{getElementById:id=>elements.get(id),querySelectorAll:selector=>selector==='.feature'?htmlCards:[]},window:{addEventListener:(name,fn)=>windowHandlers[name]=fn,print(){}},location};
  vm.runInNewContext(scripts[0],sandbox,{timeout:1000});
  const set=(id,value)=>{elements.get(id).value=value;elements.get(id).handlers.input();};
  const visible=()=>htmlCards.filter(c=>!c.hidden);
  set('f-search','TRU01');assertUI('ID search returns one target',visible().length===1&&visible()[0].id==='TRU01');
  set('f-search','zzzz_nonexistent_feature_zzzz');assertUI('Empty results and visible empty message',visible().length===0&&elements.get('f-empty').style.display==='block');
  elements.get('f-reset').handlers.click();assertUI('Reset restores all 120 cards',visible().length===120);
  set('f-milestone','M0');assertUI('Milestone filter matches registry',visible().length===features.filter(f=>f.milestone==='M0').length);
  elements.get('f-reset').handlers.click();set('f-category','LRN');set('f-state','есть');
  assertUI('Combined filters match registry',visible().length===features.filter(f=>f.category==='LRN'&&f.state==='есть').length);
  location.hash='#TRU01';windowHandlers.hashchange();assertUI('Dependency deep link clears filters and opens target',visible().length===120&&elements.get('TRU01').open&&elements.get('TRU01').scrolled);
  windowHandlers.beforeprint();assertUI('Print preparation expands all cards',htmlCards.every(c=>c.open&&!c.hidden));
  windowHandlers.afterprint();assertUI('Print cleanup restores expansion state',elements.get('TRU01').open&&htmlCards.filter(c=>c.open).length===1);
} catch(e){errors.push('Filter logic exception: '+e.stack);}
const result={checked_at:'2026-10-09',static:{passed:errors.length===0,html_files:checks,feature_cards:cards,dependency_cycles:errors.filter(e=>e.startsWith('Dependency cycle')),inline_scripts_parse:true,errors},filter_logic_node_simulation:{scope:'Actual inline script, in-memory DOM stub; not browser E2E',tests:uiTests},browser:{status:'blocked',reason:'Browser Use URL policy rejected opening file:///C:/Users/ZMII/Documents/ChatGPT/mem-tool/review/2026-10-09/mindmap-research/REPORT_RU.html',visual_check_performed:false,workaround_attempted:false,scope:'Research report only; not application E2E'}};
fs.writeFileSync(path.join(dir,'QA.json'),JSON.stringify(result,null,2)+'\n','utf8');
console.log(JSON.stringify(result,null,2));
if(errors.length)process.exitCode=1;
