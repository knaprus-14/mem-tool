(function(root){
  'use strict';
  function tree(nodes,rootID){
    const byID=new Map(nodes.map(n=>[n.id,n])),children=new Map(),descendants=new Map();
    for(const n of nodes){if(!children.has(n.parent_id))children.set(n.parent_id,[]);children.get(n.parent_id).push(n);}
    for(const list of children.values())list.sort((a,b)=>a.position-b.position||a.id.localeCompare(b.id));
    function count(id){let size=0;for(const n of children.get(id)||[])size+=1+count(n.id);descendants.set(id,size);return size;}
    count(rootID);return {byID,children,descendants,rootID};
  }
  // Root is depth zero; one level includes its immediate children.
  function collapsedAtDepth(tree,depth){
    const collapsed=new Set();
    function visit(id,level){
      const kids=tree.children.get(id)||[];
      if(kids.length&&level>=depth)collapsed.add(id);
      for(const child of kids)visit(child.id,level+1);
    }
    visit(tree.rootID,0);return collapsed;
  }
  function initialCollapsed(tree){return collapsedAtDepth(tree,1);}
  // Positions use the box's left edge and vertical centre. Boxes are 210x60;
  // togglePositions are absolute circle centres (radius 17), including folded
  // nodes. Keeping controls in the layout prevents orientation-dependent overlap.
  function layout(tree,collapsed,mode='right'){
    if(!['right','down','bilateral'].includes(mode))mode='right';
    const positions=new Map(),visible=[],sides=new Map(),togglePositions=new Map(),edgePaths=new Map();
    let leaf=0,maxDepth=0,width,height;
    const kidsOf=id=>collapsed.has(id)?[]:tree.children.get(id)||[];
    if(mode==='bilateral'){
      const leaves={1:0,'-1':0};
      function place(id,depth,side){
        visible.push(tree.byID.get(id));sides.set(id,side);
        const ys=kidsOf(id).map((n,i)=>place(n.id,depth+1,depth===0?(i%2?-1:1):side));
        const y=depth===0?54:ys.length?(ys[0]+ys[ys.length-1])/2:leaves[side]++*88+54;
        positions.set(id,{x:side*depth*290,y});return y;
      }
      place(tree.rootID,0,0);
      leaf=Math.max(1,leaves[1],leaves[-1]);
      for(const [id,p] of positions){
        const side=sides.get(id);
        p.y+=side?(leaf-leaves[side])*44:(leaf-1)*44;
      }
      // Left-pointing controls extend 35px beyond their boxes. Reserve their
      // complete bounds even when their descendants are currently hidden.
      let left=0;
      for(const [id,p] of positions)left=Math.min(left,p.x-(sides.get(id)===-1&&(tree.children.get(id)||[]).length?35:0));
      for(const p of positions.values())p.x+=36-left;
    }else{
      function place(id,depth){
        visible.push(tree.byID.get(id));maxDepth=Math.max(maxDepth,depth);
        const axes=kidsOf(id).map(n=>place(n.id,depth+1));
        const axis=axes.length?(axes[0]+axes[axes.length-1])/2:leaf++*(mode==='down'?258:88);
        positions.set(id,mode==='down'?{x:axis+36,y:depth*148+54}:{x:depth*290+36,y:axis+54});return axis;
      }
      place(tree.rootID,0);
    }
    for(const n of visible){
      if(!(tree.children.get(n.id)||[]).length)continue;
      const p=positions.get(n.id),vertical=mode==='down'||mode==='bilateral'&&n.id===tree.rootID;
      togglePositions.set(n.id,vertical?{x:p.x+105,y:p.y+48}:{x:p.x+(mode==='bilateral'&&sides.get(n.id)===-1?-18:228),y:p.y});
    }
    for(const n of visible){
      const p=positions.get(n.id),parent=positions.get(n.parent_id);if(!parent)continue;
      if(mode==='down'){
        const sx=parent.x+105,sy=parent.y+65,ex=p.x+105,ey=p.y-30,mid=(sy+ey)/2;
        edgePaths.set(n.id,`M ${sx} ${sy} C ${sx} ${mid},${ex} ${mid},${ex} ${ey}`);
      }else{
        const left=mode==='bilateral'&&sides.get(n.id)===-1,root=mode==='bilateral'&&n.parent_id===tree.rootID;
        const sx=parent.x+(left?(root?0:-35):(root?210:245)),ex=p.x+(left?210:0),mid=(sx+ex)/2;
        edgePaths.set(n.id,`M ${sx} ${parent.y} C ${mid} ${parent.y},${mid} ${p.y},${ex} ${p.y}`);
      }
    }
    if(mode==='right'){
      // Preserve the established rightward viewport and node coordinates.
      width=(maxDepth+1)*290+36;height=Math.max(160,leaf*88+40);
    }else{
      width=0;height=160;
      for(const p of positions.values()){width=Math.max(width,p.x+210+36);height=Math.max(height,p.y+30+40);}
      for(const p of togglePositions.values()){width=Math.max(width,p.x+17+36);height=Math.max(height,p.y+17+40);}
    }
    return {positions,visible,width,height,mode,edgePaths,togglePositions};
  }
  function visibleAncestor(tree,layout,id){while(id&&!layout.positions.has(id))id=tree.byID.get(id)?.parent_id;return id||tree.rootID;}
  function reveal(tree,collapsed,id){
    const next=new Set(collapsed);
    for(let parent=tree.byID.get(id)?.parent_id;parent;parent=tree.byID.get(parent)?.parent_id)next.delete(parent);
    return next;
  }
  function search(tree,query){
    const normalize=text=>text.toLowerCase().replaceAll('ё','е');
    const needle=normalize(query.trim());if(!needle)return [];
    const results=[];
    function visit(id){
      const n=tree.byID.get(id);
      for(const field of ['label','summary','body']){
        const text=n[field]||'',index=normalize(text).indexOf(needle);if(index<0)continue;
        const start=Math.max(0,index-45),end=Math.min(text.length,Math.max(start+180,index+needle.length));
        results.push({id,field,preview:(start?'…':'')+text.slice(start,end)+(end<text.length?'…':'')});break;
      }
      for(const child of tree.children.get(id)||[])visit(child.id);
    }
    visit(tree.rootID);return results;
  }
  function ancestors(tree,id){
    const path=[];for(let parent=tree.byID.get(id)?.parent_id;parent;parent=tree.byID.get(parent)?.parent_id)path.push(tree.byID.get(parent).label);
    return path.reverse();
  }
  function parentChoices(tree,excludedID,query=''){
    const normalize=s=>s.toLowerCase().replaceAll('ё','е'),needle=normalize(query.trim()),result=[];
    function visit(id,path){
      if(id===excludedID)return;const n=tree.byID.get(id),next=[...path,n.label],label=next.join(' / ');
      if(!n.locked&&normalize(label).includes(needle))result.push({id,label});
      for(const child of tree.children.get(id)||[])visit(child.id,next);
    }
    visit(tree.rootID,[]);return result;
  }
  const api={tree,initialCollapsed,collapsedAtDepth,layout,visibleAncestor,reveal,search,ancestors,parentChoices};
  if(typeof module==='object'&&module.exports)module.exports=api;else root.MemMapLayout=api;
})(typeof window==='undefined'?undefined:window);
