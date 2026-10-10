(function(root){
  'use strict';
  const names={right:'Вправо',down:'Сверху вниз',bilateral:'В обе стороны'};
  const esc=s=>String(s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&apos;'}[c]));
  function svg(m,engine){
    engine=engine||root.MemMapLayout;const layout=engine.layout(m.tree,m.collapsed,m.layoutMode),mode=layout.mode;
    const metadata={schema_version:1,map_id:m.view.map.id,map_revision:m.view.map.revision,layout:mode,scope:'visible_nodes',total_nodes:m.view.nodes.length,visible_node_ids:layout.visible.map(n=>n.id)};
    const title=m.view.map.title+' · '+names[mode]+' · '+layout.visible.length+' из '+m.view.nodes.length+' узлов';
    const output=['<?xml version="1.0" encoding="UTF-8"?>','<svg xmlns="http://www.w3.org/2000/svg" width="'+layout.width+'" height="'+layout.height+'" viewBox="0 0 '+layout.width+' '+layout.height+'" role="img">','<title>'+esc(title)+'</title>','<desc>Только видимые узлы выбранной раскладки. Свёрнутые ветви и оригиналы документов не включены. Полная карта доступна отдельной командой экспорта.</desc>','<metadata>'+esc(JSON.stringify(metadata))+'</metadata>','<style>.edge{fill:none;stroke:#8bae9e;stroke-width:2}.node{fill:#fff;stroke:#8bae9e;stroke-width:1.5}.label{fill:#203832;font:14px system-ui,sans-serif}.hint,.caption{fill:#526c60;font:11px system-ui,sans-serif}.toggle{fill:#fff;stroke:#167456;stroke-width:1.5}.symbol{fill:#167456;font:bold 19px system-ui,sans-serif}</style>','<rect width="100%" height="100%" fill="#f3f7f4"/>','<text class="caption" x="12" y="15">'+esc(names[mode]+' · Видимых узлов: '+layout.visible.length+' / '+m.view.nodes.length+' · Ревизия '+m.view.map.revision)+'</text>'];
    for(const n of layout.visible){const d=layout.edgePaths.get(n.id);if(d)output.push('<path class="edge" d="'+esc(d)+'"/>');}
    for(const n of layout.visible){const p=layout.positions.get(n.id),kids=m.tree.children.get(n.id)||[],chars=Array.from(n.label),label=chars.length>23?chars.slice(0,22).join('')+'…':n.label;
      output.push('<g transform="translate('+p.x+','+(p.y-30)+')"><title>'+esc(n.label)+'</title><rect class="node" width="210" height="60" rx="9"/><svg x="12" y="7" width="186" height="24" overflow="hidden"><text class="label" x="0" y="18">'+esc(label)+'</text></svg><text class="hint" x="12" y="45">'+esc(kids.length?(m.collapsed.has(n.id)?'Скрыто узлов: '+m.tree.descendants.get(n.id):'Ветвей: '+kids.length):'Источников: '+n.source_count)+'</text></g>');
      const t=layout.togglePositions.get(n.id);if(t)output.push('<g transform="translate('+t.x+','+t.y+')"><circle class="toggle" r="17"/><text class="symbol" x="0" y="5" text-anchor="middle">'+(m.collapsed.has(n.id)?'+':'−')+'</text></g>');
    }
    output.push('</svg>');return output.join('\n');
  }
  function install({state,$,node,run}){
    $('map-view-svg').addEventListener('click',run(()=>{const m=state.map;if(!m||state.view!=='maps'||m.selected!==state.selected||m.revision!==state.revision||!state.session?.authenticated||m.owner!==(state.session.user||'')||m.csrf!==(state.session.csrf||'')||m.displayMode!=='diagram')return;
      const text=svg(m),blob=new Blob([text],{type:'image/svg+xml;charset=utf-8'}),url=URL.createObjectURL(blob),link=node('a');link.href=url;link.download='mindmap-visible-'+(m.layoutMode||'right')+'.svg';link.click();setTimeout(()=>URL.revokeObjectURL(url),30000);
    }));
  }
  if(typeof module==='object'&&module.exports)module.exports={svg};else {root.MemMapViewExport={svg};root.installMemMapViewExport=install;}
})(typeof window==='undefined'?globalThis:window);
