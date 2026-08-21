package mem

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"
	"time"
)

const KnowledgeMapViewVersion = 1

type KnowledgeMapViewData struct {
	Version     int                        `json:"version"`
	GeneratedAt string                     `json:"generated_at"`
	Graph       KnowledgeGraph             `json:"graph"`
	Review      KnowledgeReviewReport      `json:"review"`
	Merges      []KnowledgeNodeMergeRecord `json:"merges"`
	Layout      *KnowledgeMapLayout        `json:"layout,omitempty"`
	Workspace   *KnowledgeMapWorkspace     `json:"workspace,omitempty"`
}

// KnowledgeMapWorkspace contains the short-lived capability required by the
// live page to save only its own visual layout. It is never added to an
// exported standalone HTML file.
type KnowledgeMapWorkspace struct {
	SessionToken string `json:"session_token"`
	ViewName     string `json:"view_name"`
}

func (s *Store) BuildKnowledgeMapViewData() (KnowledgeMapViewData, error) {
	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	review, err := s.ReviewKnowledgeGraph()
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	merges, err := s.ListKnowledgeNodeMerges(1000)
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	layout, err := s.LoadKnowledgeMapLayout(DefaultKnowledgeMapView)
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	return KnowledgeMapViewData{
		Version: KnowledgeMapViewVersion, GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Graph: graph, Review: review, Merges: merges, Layout: layout,
	}, nil
}

func WriteKnowledgeMapHTML(w io.Writer, title string, data KnowledgeMapViewData) error {
	if w == nil {
		return fmt.Errorf("knowledge map HTML writer is nil")
	}
	if data.Version != KnowledgeMapViewVersion {
		return fmt.Errorf("unsupported knowledge map view version %d", data.Version)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "mem-tool — Knowledge Map"
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode knowledge map data: %w", err)
	}
	if _, err := io.WriteString(w, strings.Replace(knowledgeMapHTMLPrefix, "{{TITLE}}", html.EscapeString(title), 1)); err != nil {
		return fmt.Errorf("write knowledge map HTML: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("write knowledge map payload: %w", err)
	}
	if _, err := io.WriteString(w, knowledgeMapHTMLSuffix); err != nil {
		return fmt.Errorf("write knowledge map HTML: %w", err)
	}
	return nil
}

const knowledgeMapHTMLPrefix = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'">
<title>{{TITLE}}</title>
<style>
:root{color-scheme:dark;--bg:#0a0c10;--panel:#11151b;--line:#2a313c;--text:#edf1f7;--muted:#929cab;--red:#ff4b4b;--yellow:#ffc857;--blue:#59a7ff;--green:#55d68b;--violet:#b392f0}
*{box-sizing:border-box}html,body{width:100%;height:100%;margin:0;overflow:hidden;background:var(--bg);color:var(--text);font:13px/1.4 ui-monospace,SFMono-Regular,Consolas,monospace}
button,input{font:inherit;color:inherit}button{background:#171c24;border:1px solid #384250;padding:6px 9px;cursor:pointer}button:hover{border-color:#7f8b9a}.live-only,.workspace-only{display:none}.live .live-only,.workspace .workspace-only{display:inline-block}.live-mark{color:var(--green);margin-left:7px}.save-state{align-self:center;color:var(--muted);font-size:10px;min-width:70px;text-align:right}.save-state.error{color:var(--red)}.save-state.saved{color:var(--green)}.app{height:100%;display:grid;grid-template-columns:260px minmax(320px,1fr) 340px;grid-template-rows:48px 1fr}
header{grid-column:1/4;border-bottom:1px solid var(--line);display:flex;align-items:center;gap:16px;padding:0 16px;background:#0e1116}.brand{font-weight:800;letter-spacing:.08em}.summary{color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.tools{margin-left:auto;display:flex;gap:6px}
aside{background:var(--panel);overflow:auto}.filters{border-right:1px solid var(--line);padding:14px}.details{border-left:1px solid var(--line);padding:15px}.stage{position:relative;overflow:hidden}.empty{position:absolute;inset:0;display:grid;place-items:center;color:var(--muted);pointer-events:none}.hidden{display:none!important}
h2{font-size:12px;letter-spacing:.1em;text-transform:uppercase;margin:18px 0 8px;color:#c8d0dc}.search{width:100%;background:#080a0d;border:1px solid #384250;padding:9px;outline:none}.search:focus{border-color:var(--blue)}.filter-list{display:grid;gap:6px}.filter{display:flex;align-items:center;gap:8px;color:#c6ced8}.filter input{accent-color:var(--blue)}.swatch{width:10px;height:10px;border:1px solid #fff3}.count{margin-left:auto;color:var(--muted)}
.stat-grid{display:grid;grid-template-columns:1fr 1fr;gap:6px}.stat{border:1px solid var(--line);padding:7px}.stat b{display:block;font-size:16px}.stat span{color:var(--muted)}
svg{display:block;width:100%;height:100%;touch-action:none}.edge{stroke:#6b7685;stroke-opacity:.55;stroke-width:1.3}.edge.alert{stroke:var(--red);stroke-width:2}.edge-hit{stroke:transparent;stroke-width:12;cursor:pointer}.node{cursor:pointer}.node-shape{stroke:#0a0c10;stroke-width:2}.node.selected .node-shape{stroke:#fff;stroke-width:3}.node-label{font-size:11px;fill:#e9edf4;paint-order:stroke;stroke:#090b0e;stroke-width:4;stroke-linejoin:round;pointer-events:none}.node.dim,.edge.dim,.edge-hit.dim{opacity:.08}.node.match .node-shape{stroke:var(--yellow);stroke-width:4}.edge.selected{stroke:#fff;stroke-width:3;stroke-opacity:1}
.detail-title{font-size:18px;font-weight:800;line-height:1.2;word-break:break-word}.badges{display:flex;flex-wrap:wrap;gap:5px;margin:10px 0}.badge{border:1px solid var(--line);padding:2px 6px;color:#c9d1dc}.badge.current{border-color:#287c50;color:var(--green)}.badge.stale,.badge.missing{border-color:#8a4a25;color:var(--yellow)}.badge.contradiction{border-color:#8d3030;color:var(--red)}.badge.gap{border-color:#826a1c;color:var(--yellow)}
.field{margin:12px 0}.field-name{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.08em}.field-value{white-space:pre-wrap;overflow-wrap:anywhere}.evidence{border:1px solid var(--line);margin:10px 0;padding:9px}.excerpt{white-space:pre-wrap;margin:7px 0;color:#d9e0e9}.coordinates{color:var(--blue);overflow-wrap:anywhere}.technical{margin:12px 0;border:1px solid var(--line);padding:7px 9px;color:#8e99a8}.technical summary{cursor:pointer;color:#aab4c2;user-select:none}.technical .field{margin:8px 0}.technical .field-value{font-size:10px}.hash{font-size:10px;color:#737f8e;overflow-wrap:anywhere}.merge{border-left:3px solid var(--violet);padding-left:8px}.footer-note{color:var(--muted);font-size:11px;margin-top:18px}
@media(max-width:1050px){.app{grid-template-columns:220px 1fr}.details{position:absolute;right:0;top:48px;bottom:0;width:340px;z-index:5;box-shadow:-10px 0 30px #0008}.details.closed{display:none}header{grid-column:1/3}}
@media(max-width:700px){.app{display:block}.filters{position:absolute;left:0;top:48px;bottom:0;width:250px;z-index:6}.filters.closed{display:none}.stage{position:absolute;inset:48px 0 0}.details{width:min(92vw,360px)}header{height:48px}.summary{display:none}}
</style>
</head>
<body>
<div class="app" data-mem-map="v1">
<header><div class="brand">MEM / KNOWLEDGE MAP<span class="live-only live-mark">LIVE</span></div><div id="summary" class="summary"></div><div class="tools"><span id="saveState" class="workspace-only save-state"></span><button id="resetLayoutBtn" class="workspace-only">СБРОСИТЬ ВИД</button><button id="refreshBtn" class="live-only">ОБНОВИТЬ</button><button id="filtersBtn">FILTERS</button><button id="minusBtn">−</button><button id="fitBtn">FIT</button><button id="plusBtn">+</button><button id="detailsBtn">DETAILS</button></div></header>
<aside id="filters" class="filters"><input id="search" class="search" placeholder="Поиск /" autocomplete="off"><div id="stats" class="stat-grid"></div><h2>Статусы</h2><div id="statusFilters" class="filter-list"></div><h2>Evidence</h2><div id="evidenceFilters" class="filter-list"></div><h2>Типы узлов</h2><div id="kindFilters" class="filter-list"></div><h2>Связи</h2><div id="relationFilters" class="filter-list"></div><div class="footer-note">Колесо — масштаб · drag фона — pan · drag узла — закрепить · двойной щелчок — освободить · / — поиск · Esc — сброс выбора</div></aside>
<main class="stage"><svg id="graph" role="img" aria-label="Интерактивная карта знаний"><defs><marker id="arrow" viewBox="0 -5 10 10" refX="19" refY="0" markerWidth="6" markerHeight="6" orient="auto"><path d="M0,-5L10,0L0,5" fill="#6b7685"></path></marker></defs><g id="viewport"><g id="edges"></g><g id="nodes"></g></g></svg><div id="empty" class="empty hidden">Нет объектов для выбранных фильтров</div></main>
<aside id="details" class="details"><div class="detail-title">Выберите узел или связь</div><div class="footer-note">Здесь появятся точные document/page/block/chunk coordinates, выдержка, revision и evidence hash.</div></aside>
</div>
<script id="mem-map-data" type="application/json">`

const knowledgeMapHTMLSuffix = `</script>
<script>
(()=>{'use strict';
const data=JSON.parse(document.getElementById('mem-map-data').textContent),graph=data.graph||{},nodes=(graph.nodes||[]).map((n,i)=>({...n,_i:i,_x:0,_y:0,_vx:0,_vy:0,_fixed:false})),edges=(graph.edges||[]).map(e=>({...e}));
const $=id=>document.getElementById(id),svg=$('graph'),viewport=$('viewport'),edgeLayer=$('edges'),nodeLayer=$('nodes'),details=$('details'),search=$('search');
const liveMode=location.protocol==='http:'||location.protocol==='https:',workspace=data.workspace||null,writable=!!(liveMode&&workspace?.session_token),savedLayout=data.layout?.version===1?data.layout:null;if(liveMode)document.documentElement.classList.add('live');if(writable)document.documentElement.classList.add('workspace');
const review=new Map((data.review?.items||[]).map(x=>[x.object_type+':'+x.id,x])),nodeById=new Map(nodes.map(n=>[n.id,n])),mergeBySource=new Map((data.merges||[]).filter(m=>m.current).map(m=>[m.source_id,m]));
const palette={document:'#59a7ff',topic:'#b392f0',claim:'#55d68b',note:'#9aa5b1',question:'#70d6ff',card:'#ff8fab',contradiction:'#ff4b4b',gap:'#ffc857'};
const relationColor={contradicts:'#ff4b4b',reveals_gap:'#ffc857',supports:'#55d68b',derived_from:'#b392f0',contains:'#59a7ff'};
let W=1,H=1,scale=1,tx=0,ty=0,selected=null,drag=null,pan=null,running=true,saveTimer=null,saving=false,saveAgain=false;
const ns='http://www.w3.org/2000/svg',make=(tag,attrs={})=>{const x=document.createElementNS(ns,tag);for(const[k,v]of Object.entries(attrs))x.setAttribute(k,v);return x};
const escText=v=>v==null?'':String(v),reviewFor=(type,id)=>review.get(type+':'+id),evidenceState=(type,id)=>reviewFor(type,id)?.evidence_state||'missing';
function seed(){const radius=Math.max(120,Math.min(W,H)*.34);nodes.forEach((n,i)=>{const p=savedLayout?.nodes?.[n.id];if(p){n._x=p.x;n._y=p.y;n._fixed=!!p.pinned;return}const a=(i/Math.max(1,nodes.length))*Math.PI*2;n._x=W/2+Math.cos(a)*radius;n._y=H/2+Math.sin(a)*radius});if(savedLayout?.viewport){scale=savedLayout.viewport.scale;tx=savedLayout.viewport.x;ty=savedLayout.viewport.y}}
function nodePath(n){if(n.kind==='contradiction')return'M0,-15L15,0L0,15L-15,0Z';if(n.kind==='gap')return'M-14,-8L0,-16L14,-8L14,8L0,16L-14,8Z';if(n.kind==='document')return'M-13,-15H8L14,-9V15H-13Z';if(n.kind==='topic')return'M-15,-12H15V12H-15Z';return'M0,-14A14,14 0 1 1 0,14A14,14 0 1 1 0,-14'}
function initGraph(){edgeLayer.textContent='';nodeLayer.textContent='';edges.forEach(e=>{const g=make('g'),line=make('line',{class:'edge'+((e.kind==='contradicts'||e.kind==='reveals_gap')?' alert':''),'marker-end':'url(#arrow)'}),hit=make('line',{class:'edge-hit'});line.style.stroke=relationColor[e.kind]||'';g.append(line,hit);g.addEventListener('click',ev=>{ev.stopPropagation();selectObject('edge',e)});e._g=g;e._line=line;e._hit=hit;edgeLayer.append(g)});nodes.forEach(n=>{const g=make('g',{class:'node',tabindex:'0'}),shape=make('path',{class:'node-shape',d:nodePath(n),fill:palette[n.kind]||'#9aa5b1'}),label=make('text',{class:'node-label',x:19,y:4});label.textContent=n.label||n.id;g.append(shape,label);g.addEventListener('pointerdown',ev=>startNodeDrag(ev,n));g.addEventListener('click',ev=>{ev.stopPropagation();selectObject('node',n)});g.addEventListener('dblclick',ev=>{ev.stopPropagation();n._fixed=false;scheduleLayoutSave()});g.addEventListener('keydown',ev=>{if(ev.key==='Enter')selectObject('node',n)});n._g=g;nodeLayer.append(g)});applyFilters()}
function tick(){if(running){const n=nodes.length,step=Math.max(1,Math.ceil(n/260));for(const e of edges){const a=nodeById.get(e.from),b=nodeById.get(e.to);if(!a||!b)continue;let dx=b._x-a._x,dy=b._y-a._y,d=Math.hypot(dx,dy)||1,f=Math.max(-1.5,Math.min(1.5,(d-145)*.006)),fx=dx/d*f,fy=dy/d*f;a._vx+=fx;a._vy+=fy;b._vx-=fx;b._vy-=fy}for(let i=0;i<n;i++){const a=nodes[i];a._vx+=(W/2-a._x)*.0006;a._vy+=(H/2-a._y)*.0006;for(let j=i+step;j<n;j+=step){const b=nodes[j],dx=b._x-a._x,dy=b._y-a._y,d2=dx*dx+dy*dy+100,f=1800/(d2*Math.sqrt(d2));a._vx-=dx*f;a._vy-=dy*f;b._vx+=dx*f;b._vy+=dy*f}if(!a._fixed){a._vx=Math.max(-5,Math.min(5,a._vx*.86));a._vy=Math.max(-5,Math.min(5,a._vy*.86));a._x+=a._vx;a._y+=a._vy}}}render();requestAnimationFrame(tick)}
function render(){for(const e of edges){const a=nodeById.get(e.from),b=nodeById.get(e.to);if(!a||!b)continue;for(const l of[e._line,e._hit]){l.setAttribute('x1',a._x);l.setAttribute('y1',a._y);l.setAttribute('x2',b._x);l.setAttribute('y2',b._y)}}for(const n of nodes)n._g.setAttribute('transform','translate('+n._x+' '+n._y+')');viewport.setAttribute('transform','translate('+tx+' '+ty+') scale('+scale+')')}
function values(items,key){return[...new Set(items.map(x=>x[key]).filter(Boolean))].sort()}
function checkbox(parent,value,count,checked=true){const label=document.createElement('label');label.className='filter';const input=document.createElement('input');input.type='checkbox';input.value=value;input.checked=checked;const text=document.createElement('span');text.textContent=value;const c=document.createElement('span');c.className='count';c.textContent=count;label.append(input,text,c);$(parent).append(label);input.addEventListener('change',applyFilters)}
function buildFilters(){for(const v of['active','draft','resolved'])checkbox('statusFilters',v,nodes.filter(n=>n.status===v).length);for(const v of['current','stale','missing'])checkbox('evidenceFilters',v,nodes.filter(n=>evidenceState('node',n.id)===v).length);for(const v of values(nodes,'kind'))checkbox('kindFilters',v,nodes.filter(n=>n.kind===v).length);for(const v of values(edges,'kind'))checkbox('relationFilters',v,edges.filter(e=>e.kind===v).length);const s=data.review?.summary||{};$('stats').innerHTML='<div class="stat"><b>'+nodes.length+'</b><span>nodes</span></div><div class="stat"><b>'+edges.length+'</b><span>edges</span></div><div class="stat"><b>'+(s.stale_evidence||0)+'</b><span>stale</span></div><div class="stat"><b>'+(s.missing_evidence||0)+'</b><span>missing</span></div>';$('summary').textContent=nodes.length+' nodes · '+edges.length+' relations · '+(data.generated_at||'')}
const checked=id=>new Set([...$(id).querySelectorAll('input:checked')].map(x=>x.value));
function applyFilters(){const statuses=checked('statusFilters'),states=checked('evidenceFilters'),kinds=checked('kindFilters'),rels=checked('relationFilters'),q=search.value.trim().toLocaleLowerCase();let visible=0;for(const n of nodes){const hit=!q||(n.id+' '+(n.label||'')+' '+(n.body||'')+' '+n.kind).toLocaleLowerCase().includes(q),show=hit&&statuses.has(n.status)&&states.has(evidenceState('node',n.id))&&kinds.has(n.kind);n._visible=show;n._g.classList.toggle('dim',!show);n._g.classList.toggle('match',!!q&&hit);if(show)visible++}for(const e of edges){const show=rels.has(e.kind)&&nodeById.get(e.from)?._visible&&nodeById.get(e.to)?._visible;e._g.classList.toggle('dim',!show)}$('empty').classList.toggle('hidden',visible>0)}
function field(name,value){const d=document.createElement('div');d.className='field';const n=document.createElement('div');n.className='field-name';n.textContent=name;const v=document.createElement('div');v.className='field-value';v.textContent=escText(value);d.append(n,v);return d}
const badgeLabels={node:'узел',edge:'связь',document:'документ',topic:'тема',claim:'утверждение',note:'заметка',question:'вопрос',card:'карточка',contradiction:'противоречие',gap:'пробел в знаниях',active:'активно',draft:'черновик',resolved:'решено',manual:'создано вручную',generated:'создано моделью',current:'источник актуален',stale:'источник устарел',missing:'источник не найден'};
function badge(value){const b=document.createElement('span');b.className='badge '+value;b.textContent=badgeLabels[value]||value;return b}
function technical(fields,summary='Технические данные'){const d=document.createElement('details');d.className='technical';const s=document.createElement('summary');s.textContent=summary;d.append(s);for(const [name,value]of fields)if(value)d.append(field(name,value));return d}
function showEvidence(item){const wrap=document.createElement('div');const r=reviewFor(item.from?'edge':'node',item.id);for(const ev of(r?.evidence||[])){const box=document.createElement('div');box.className='evidence';const badges=document.createElement('div');badges.className='badges';badges.append(badge(ev.state));const coord=document.createElement('div');coord.className='coordinates';const a=ev.anchor;coord.textContent=a.source_path+' · страница '+(a.page||'?')+' · блок '+a.block_index+' · фрагмент '+a.block_chunk_index;const ex=document.createElement('div');ex.className='excerpt';ex.textContent=a.excerpt;box.append(badges,coord,ex,technical([['Citation ID',a.citation_id],['Ревизия документа',a.document_revision],['Хеш чанка',a.chunk_hash],['Хеш evidence',a.evidence_hash]],'Технические данные источника'));wrap.append(box)}return wrap}
function selectObject(type,item){selected={type,item};document.querySelectorAll('.selected').forEach(x=>x.classList.remove('selected'));if(type==='node')item._g.classList.add('selected');else item._line.classList.add('selected');details.textContent='';const title=document.createElement('div');title.className='detail-title';title.textContent=item.label||item.id;const badges=document.createElement('div');badges.className='badges';for(const v of[type,item.kind,item.status,item.origin,evidenceState(type,item.id)])if(v)badges.append(badge(v));details.append(title,badges);if(type==='edge'){const from=nodeById.get(item.from),to=nodeById.get(item.to);details.append(field('Связь',(from?.label||item.from)+' → '+(to?.label||item.to)))}if(item.body)details.append(field('Описание',item.body));details.append(field('Уверенность',item.confidence||0),field('Обновлено',item.updated));const merge=mergeBySource.get(item.id);if(merge){const target=nodeById.get(merge.target_id);const m=document.createElement('div');m.className='field merge';m.append(field('Объединено с',target?.label||merge.target_id),field('Проверил',merge.reviewer),field('Сходство',merge.similarity));details.append(m)}const r=reviewFor(type,item.id);details.append(showEvidence(item),technical([['Внутренний ID',item.id],['Evidence digest',r?.evidence_digest],['Исходный узел',type==='edge'?item.from:''],['Целевой узел',type==='edge'?item.to:'']]));details.classList.remove('closed')}
function setSaveState(text,state=''){const el=$('saveState');el.textContent=text;el.className='workspace-only save-state '+state}
function layoutPayload(){return{version:1,nodes:Object.fromEntries(nodes.map(n=>[n.id,{x:n._x,y:n._y,pinned:n._fixed}])),viewport:{scale:scale,x:tx,y:ty}}}
function scheduleLayoutSave(delay=500){if(!writable)return;clearTimeout(saveTimer);setSaveState('ИЗМЕНЕНО');saveTimer=setTimeout(saveLayout,delay)}
async function saveLayout(){if(!writable)return;if(saving){saveAgain=true;return}saving=true;setSaveState('СОХРАНЕНИЕ…');try{const response=await fetch('/api/layout',{method:'PUT',headers:{'Content-Type':'application/json','X-Mem-Session':workspace.session_token},body:JSON.stringify(layoutPayload())});if(!response.ok)throw new Error((await response.text()).trim()||('HTTP '+response.status));await response.json();setSaveState('СОХРАНЕНО','saved')}catch(error){console.error('mem map layout save failed',error);setSaveState('ОШИБКА','error')}finally{saving=false;if(saveAgain){saveAgain=false;scheduleLayoutSave(0)}}}
async function resetLayout(){if(!writable||!confirm('Сбросить сохранённое расположение узлов и масштаб?'))return;setSaveState('СБРОС…');try{const response=await fetch('/api/layout',{method:'DELETE',headers:{'X-Mem-Session':workspace.session_token}});if(!response.ok)throw new Error((await response.text()).trim()||('HTTP '+response.status));location.reload()}catch(error){console.error('mem map layout reset failed',error);setSaveState('ОШИБКА','error')}}
function point(ev){const r=svg.getBoundingClientRect();return{x:ev.clientX-r.left,y:ev.clientY-r.top}}
function startNodeDrag(ev,n){ev.stopPropagation();const p=point(ev);drag={n,dx:(p.x-tx)/scale-n._x,dy:(p.y-ty)/scale-n._y};n._fixed=true;n._g.setPointerCapture(ev.pointerId)}
function endPointerInteraction(){const changed=!!drag||!!pan;if(drag){drag.n._vx=0;drag.n._vy=0;drag=null}pan=null;if(changed)scheduleLayoutSave()}
svg.addEventListener('pointerdown',ev=>{if(ev.target===svg){const p=point(ev);pan={x:p.x-tx,y:p.y-ty};svg.setPointerCapture(ev.pointerId)}});svg.addEventListener('pointermove',ev=>{const p=point(ev);if(drag){drag.n._x=(p.x-tx)/scale-drag.dx;drag.n._y=(p.y-ty)/scale-drag.dy}else if(pan){tx=p.x-pan.x;ty=p.y-pan.y}});svg.addEventListener('pointerup',endPointerInteraction);svg.addEventListener('pointercancel',endPointerInteraction);svg.addEventListener('click',ev=>{if(ev.target!==svg)return;selected=null;document.querySelectorAll('.selected').forEach(x=>x.classList.remove('selected'))});
function zoomAt(f,cx=W/2,cy=H/2){const old=scale;scale=Math.max(.15,Math.min(4,scale*f));tx=cx-(cx-tx)*(scale/old);ty=cy-(cy-ty)*(scale/old)}svg.addEventListener('wheel',ev=>{ev.preventDefault();const p=point(ev);zoomAt(ev.deltaY<0?1.12:.89,p.x,p.y);scheduleLayoutSave()},{passive:false});
function fit(){if(!nodes.length)return;const xs=nodes.map(n=>n._x),ys=nodes.map(n=>n._y),minX=Math.min(...xs)-50,maxX=Math.max(...xs)+50,minY=Math.min(...ys)-50,maxY=Math.max(...ys)+50;scale=Math.max(.15,Math.min(2,Math.min(W/(maxX-minX),H/(maxY-minY))));tx=(W-(minX+maxX)*scale)/2;ty=(H-(minY+maxY)*scale)/2}
$('plusBtn').onclick=()=>{zoomAt(1.25);scheduleLayoutSave()};$('minusBtn').onclick=()=>{zoomAt(.8);scheduleLayoutSave()};$('fitBtn').onclick=()=>{fit();scheduleLayoutSave()};$('detailsBtn').onclick=()=>details.classList.toggle('closed');$('filtersBtn').onclick=()=>$('filters').classList.toggle('closed');$('refreshBtn').onclick=()=>location.reload();$('resetLayoutBtn').onclick=resetLayout;search.addEventListener('input',applyFilters);document.addEventListener('keydown',ev=>{if(ev.key==='/'&&document.activeElement!==search){ev.preventDefault();search.focus()}if(ev.key==='Escape'){search.value='';applyFilters();selected=null;document.querySelectorAll('.selected').forEach(x=>x.classList.remove('selected'))}});
new ResizeObserver(()=>{const r=svg.getBoundingClientRect(),first=W===1;W=Math.max(1,r.width);H=Math.max(1,r.height);if(first){seed();if(!savedLayout)setTimeout(fit,350)}}).observe(svg);buildFilters();initGraph();tick();
})();
</script>
</body>
</html>
`
