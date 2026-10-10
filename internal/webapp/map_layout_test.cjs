const {test}=require('node:test');
const assert=require('node:assert/strict');
const M=require('./assets/map-layout.js');
const nodes=[{id:'r',parent_id:'',position:0},{id:'a',parent_id:'r',position:0},{id:'b',parent_id:'r',position:1},{id:'a1',parent_id:'a',position:0},{id:'a2',parent_id:'a',position:1},{id:'deep',parent_id:'a1',position:0}];
test('overview shows main topics and counts all hidden descendants',()=>{
 const tree=M.tree(nodes,'r'),collapsed=M.initialCollapsed(tree),layout=M.layout(tree,collapsed);
 assert.deepEqual(layout.visible.map(n=>n.id),['r','a','b']);assert.equal(tree.descendants.get('a'),3);
 assert.equal(M.visibleAncestor(tree,layout,'deep'),'a');assert.equal(M.visibleAncestor(tree,layout,'removed'),'r');
});
test('nested folding survives closing and reopening its ancestor',()=>{
 const tree=M.tree(nodes,'r'),collapsed=M.initialCollapsed(tree);collapsed.delete('a');
 assert.deepEqual(M.layout(tree,collapsed).visible.map(n=>n.id),['r','a','a1','a2','b']);
 collapsed.add('r');assert.equal(M.layout(tree,collapsed).visible.length,1);
 collapsed.delete('r');assert.equal(M.layout(tree,collapsed).visible.length,5);
 collapsed.delete('a1');assert.equal(M.layout(tree,collapsed).visible.length,6);
 assert.equal(nodes.length,6);assert.equal(nodes[3].parent_id,'a');
});
test('large map creates geometry only for visible nodes and keeps sibling order',()=>{
 const many=[{id:'r',parent_id:'',position:0}];for(let g=0;g<9;g++){many.push({id:'g'+g,parent_id:'r',position:g});for(let n=0;n<110;n++)many.push({id:`g${g}-${n}`,parent_id:'g'+g,position:n});}
 const tree=M.tree(many.reverse(),'r'),collapsed=M.initialCollapsed(tree);let layout=M.layout(tree,collapsed);
 assert.equal(many.length,1000);assert.equal(layout.visible.length,10);assert.equal(layout.positions.size,10);
 assert.deepEqual(layout.visible.map(n=>n.id),['r',...Array.from({length:9},(_,i)=>'g'+i)]);
 collapsed.delete('g4');layout=M.layout(tree,collapsed);assert.equal(layout.visible.length,120);
 assert.equal(tree.descendants.get('g4'),110);assert.ok(layout.positions.get('g4-109').y>layout.positions.get('g4-0').y);
 collapsed.clear();layout=M.layout(tree,collapsed);assert.equal(layout.visible.length,1000);
 for(const n of layout.visible){const p=layout.positions.get(n.id);assert.ok(Number.isFinite(p.x)&&Number.isFinite(p.y));if(n.parent_id)assert.ok(p.x>layout.positions.get(n.parent_id).x);}
});
test('deep tree may be unfolded to server depth limit and collapsed to root',()=>{
 const deep=Array.from({length:256},(_,i)=>({id:String(i),parent_id:i?String(i-1):'',position:0}));const tree=M.tree(deep,'0');
 assert.equal(M.layout(tree,new Set()).visible.length,256);
 const closed=M.layout(tree,new Set(['0']));assert.equal(closed.visible.length,1);assert.equal(M.visibleAncestor(tree,closed,'255'),'0');
});
test('depth presets replace manual folds and count levels below the root',()=>{
 const tree=M.tree(nodes,'r');
 assert.deepEqual(M.layout(tree,M.collapsedAtDepth(tree,1)).visible.map(n=>n.id),['r','a','b']);
 assert.deepEqual(M.layout(tree,M.collapsedAtDepth(tree,2)).visible.map(n=>n.id),['r','a','a1','a2','b']);
 assert.equal(M.layout(tree,M.collapsedAtDepth(tree,3)).visible.length,6);
 const manual=M.collapsedAtDepth(tree,1);manual.delete('a');manual.delete('a1');
 const reset=M.collapsedAtDepth(tree,2);
 assert.equal(M.visibleAncestor(tree,M.layout(tree,reset),'deep'),'a1');
 assert.ok(reset.has('a1'));assert.ok(!reset.has('a'));
 assert.deepEqual(M.initialCollapsed(tree),M.collapsedAtDepth(tree,1));
});
test('depth presets handle a large branching tree and a root without children',()=>{
 const many=[{id:'r',parent_id:'',position:0}];
 for(let a=0;a<8;a++){const x='a'+a;many.push({id:x,parent_id:'r',position:a});
  for(let b=0;b<8;b++){const y=x+'b'+b;many.push({id:y,parent_id:x,position:b});
   for(let c=0;c<8;c++)many.push({id:y+'c'+c,parent_id:y,position:c});}}
 const tree=M.tree(many,'r');
 assert.deepEqual([1,2,3].map(d=>M.layout(tree,M.collapsedAtDepth(tree,d)).visible.length),[9,73,585]);
 assert.equal(M.layout(tree,M.collapsedAtDepth(tree,1)).visible.length,9);
 const single=M.tree([many[0]],'r');for(const d of [1,2,3])assert.equal(M.layout(single,M.collapsedAtDepth(single,d)).visible.length,1);
});
test('map search includes hidden text, ignores case and yo, and treats queries literally',()=>{
 const searchable=nodes.map(n=>({...n,label:n.id}));
 searchable.find(n=>n.id==='a').label='Ёлка';
 searchable.find(n=>n.id==='a1').summary='Опыт: МОЩНОСТЬ';
 searchable.find(n=>n.id==='deep').body='До '+'.* [x] <script> ёлка '.repeat(20);
 const tree=M.tree(searchable,'r');
 assert.deepEqual(M.search(tree,' ЕЛКА ').map(h=>[h.id,h.field]),[['a','label'],['deep','body']]);
 assert.deepEqual(M.search(tree,'мощность').map(h=>h.id),['a1']);
 assert.deepEqual(M.search(tree,'.* [x] <script>').map(h=>h.id),['deep']);
 assert.deepEqual(M.search(tree,'несуществующий'),[]);assert.deepEqual(M.search(tree,' \n '),[]);
 assert.ok(!M.layout(tree,M.initialCollapsed(tree)).positions.has('deep'));
 assert.ok(M.search(tree,'<script>')[0].preview.includes('<script>'));
});
test('search reveals only the ancestor path and keeps the matched branch folded',()=>{
 const tree=M.tree(nodes.map(n=>({...n,label:n.id})),'r');
 const folded=new Set(['r','a','a1','b']);const original=[...folded];
 const revealed=M.reveal(tree,folded,'a1');
 assert.deepEqual([...folded],original);assert.deepEqual([...revealed],['a1','b']);
 assert.ok(M.layout(tree,revealed).positions.has('a1'));assert.ok(!M.layout(tree,revealed).positions.has('deep'));
 assert.deepEqual(M.ancestors(tree,'deep'),['r','a','a1']);assert.deepEqual(M.ancestors(tree,'r'),[]);
 assert.deepEqual(M.reveal(tree,folded,'missing'),folded);
});
test('search returns all matching nodes once in tree order, with bounded contextual previews',()=>{
 const many=Array.from({length:1000},(_,i)=>({id:String(i),parent_id:i?'0':'',position:i,label:i?'Тема '+i:'Карта',summary:'ключ',body:'x'.repeat(2000)+' ИСКОМОЕ '+'z'.repeat(2000)}));
 const tree=M.tree(many.reverse(),'0');const hits=M.search(tree,'ключ');
 assert.equal(hits.length,1000);assert.deepEqual(hits.slice(0,3).map(h=>h.id),['0','1','2']);
 assert.equal(new Set(hits.map(h=>h.id)).size,1000);
 const preview=M.search(tree,'искомое')[0].preview;assert.ok(preview.includes('ИСКОМОЕ'));assert.ok(preview.length<=182);
});

test('parent picker searches paths in tree order and excludes the moved subtree and locked nodes',()=>{
 const rows=nodes.map(n=>({...n,label:n.id==='b'?'Ёлка':n.id}));rows.find(n=>n.id==='a1').locked=true;
 const tree=M.tree(rows,'r');
 assert.deepEqual(M.parentChoices(tree,'a').map(n=>n.id),['r','b']);
 assert.deepEqual(M.parentChoices(tree,'b').map(n=>n.id),['r','a','deep','a2']);
 assert.deepEqual(M.parentChoices(tree,'a','ЕЛКА').map(n=>n.id),['b']);
 assert.deepEqual(M.parentChoices(tree,'b','a1').map(n=>n.id),['deep']);
 assert.deepEqual(M.parentChoices(tree,'a','missing'),[]);
});

const modes=['right','down','bilateral'];
function checkGeometry(tree,layout){
 const {positions,togglePositions,edgePaths,visible,width,height,mode}=layout;
 assert.ok(Number.isFinite(width)&&Number.isFinite(height)&&width>0&&height>0);
 assert.equal(positions.size,visible.length);assert.equal(edgePaths.size,visible.length-1);
 const boxes=visible.map(n=>{const p=positions.get(n.id);assert.ok(Number.isFinite(p.x)&&Number.isFinite(p.y));return {id:n.id,x:p.x,y:p.y-30,w:210,h:60};});
 for(const b of boxes)assert.ok(b.x>0&&b.y>0&&b.x+b.w<width&&b.y+b.h<height,`${mode}: box outside viewport ${b.id}`);
 for(let i=0;i<boxes.length;i++)for(let j=i+1;j<boxes.length;j++){
  const a=boxes[i],b=boxes[j];assert.ok(a.x+a.w<=b.x||b.x+b.w<=a.x||a.y+a.h<=b.y||b.y+b.h<=a.y,`${mode}: box overlap ${a.id}/${b.id}`);
 }
 const circles=[...togglePositions].map(([id,p])=>({id,...p}));
 for(const c of circles){
  assert.ok(c.x-17>0&&c.y-17>0&&c.x+17<width&&c.y+17<height,`${mode}: control outside viewport ${c.id}`);
  assert.ok((tree.children.get(c.id)||[]).length&&positions.has(c.id));
  for(const b of boxes){const dx=Math.max(b.x-c.x,0,c.x-b.x-b.w),dy=Math.max(b.y-c.y,0,c.y-b.y-b.h);assert.ok(dx*dx+dy*dy>=17*17,`${mode}: control overlaps box ${c.id}/${b.id}`);}
 }
 for(let i=0;i<circles.length;i++)for(let j=i+1;j<circles.length;j++)assert.ok((circles[i].x-circles[j].x)**2+(circles[i].y-circles[j].y)**2>=34**2,`${mode}: controls overlap`);
 assert.equal(circles.length,visible.filter(n=>(tree.children.get(n.id)||[]).length).length);
 for(const n of visible){
  if(n.id===tree.rootID){assert.ok(!edgePaths.has(n.id));continue;}
  const p=positions.get(n.id),parent=positions.get(n.parent_id),path=edgePaths.get(n.id);
  assert.match(path,/^M [\d.]+ [\d.]+ C [\d.]+ [\d.]+,[\d.]+ [\d.]+,[\d.]+ [\d.]+$/);
  const values=path.match(/[\d.]+/g).map(Number),[sx,sy,cx1,cy1,cx2,cy2,ex,ey]=values;
  for(let i=0;i<values.length;i+=2)assert.ok(values[i]>0&&values[i]<width&&values[i+1]>0&&values[i+1]<height);
  if(mode==='down'){
   assert.deepEqual([sx,sy],[parent.x+105,parent.y+65]);assert.deepEqual([ex,ey],[p.x+105,p.y-30]);
  }else{
   const left=p.x<parent.x,root=mode==='bilateral'&&n.parent_id===tree.rootID;
   assert.deepEqual([sx,sy],[parent.x+(left?(root?0:-35):(root?210:245)),parent.y]);assert.deepEqual([ex,ey],[p.x+(left?210:0),p.y]);
  }
  // Sample the actual cubic, including its endpoints: an edge must never run
  // through a node or control. Boundary contact at the attachment is allowed.
  for(let step=0;step<=8;step++){
   const t=step/8,u=1-t,x=u**3*sx+3*u*u*t*cx1+3*u*t*t*cx2+t**3*ex,y=u**3*sy+3*u*u*t*cy1+3*u*t*t*cy2+t**3*ey;
   for(const b of boxes)assert.ok(!(x>b.x+1e-7&&x<b.x+b.w-1e-7&&y>b.y+1e-7&&y<b.y+b.h-1e-7),`${mode}: edge ${n.id} through box ${b.id}`);
   for(const c of circles)assert.ok((x-c.x)**2+(y-c.y)**2>=17**2-1e-6,`${mode}: edge ${n.id} through control ${c.id}`);
  }
 }
}

test('right mode preserves established coordinates and viewport, with controls and paths provided',()=>{
 const tree=M.tree(nodes,'r'),layout=M.layout(tree,new Set());
 assert.deepEqual(layout.visible.map(n=>n.id),['r','a','a1','deep','a2','b']);
 assert.deepEqual(Object.fromEntries(layout.positions),{deep:{x:906,y:54},a1:{x:616,y:54},a2:{x:616,y:142},a:{x:326,y:98},b:{x:326,y:230},r:{x:36,y:164}});
 assert.equal(layout.width,1196);assert.equal(layout.height,304);
 assert.deepEqual(layout,M.layout(tree,new Set(),'right'));
 assert.deepEqual(layout,M.layout(tree,new Set(),'unknown'));
 assert.deepEqual(Object.fromEntries(layout.togglePositions),{r:{x:264,y:164},a:{x:554,y:98},a1:{x:844,y:54}});
 checkGeometry(tree,layout);
});

test('down mode preserves preorder and allocates horizontal subtree space above its children',()=>{
 const tree=M.tree(nodes,'r'),layout=M.layout(tree,new Set(),'down');
 assert.deepEqual(layout.visible.map(n=>n.id),['r','a','a1','deep','a2','b']);
 assert.deepEqual(Object.fromEntries(layout.positions),{deep:{x:36,y:498},a1:{x:36,y:350},a2:{x:294,y:350},a:{x:165,y:202},b:{x:552,y:202},r:{x:358.5,y:54}});
 assert.deepEqual(layout.togglePositions.get('r'),{x:463.5,y:102});
 checkGeometry(tree,layout);
});

test('bilateral alternates root branches and keeps every descendant on its branch side',()=>{
 const rows=[{id:'r',parent_id:'',position:0}];
 for(let i=0;i<6;i++){rows.push({id:`b${i}`,parent_id:'r',position:i});for(let j=0;j<i+1;j++)rows.push({id:`b${i}-${j}`,parent_id:`b${i}`,position:j});}
 const tree=M.tree(rows.slice().reverse(),'r'),layout=M.layout(tree,new Set(),'bilateral'),root=layout.positions.get('r');
 assert.deepEqual(layout.visible.map(n=>n.id),M.layout(tree,new Set()).visible.map(n=>n.id));
 for(let i=0;i<6;i++){
  const p=layout.positions.get(`b${i}`),sign=i%2?-1:1;assert.equal(Math.sign(p.x-root.x),sign);
  for(let j=0;j<i+1;j++)assert.equal(Math.sign(layout.positions.get(`b${i}-${j}`).x-p.x),sign);
 }
 assert.deepEqual(layout.togglePositions.get('r'),{x:root.x+105,y:root.y+48});
 checkGeometry(tree,layout);
});

test('all modes are deterministic and leave nodes, parent order and collapsed state unchanged',()=>{
 const rows=nodes.map(n=>Object.freeze({...n})),before=JSON.stringify(rows),tree=M.tree(rows,'r'),collapsed=new Set(['a1']);
 const order=[...tree.children].map(([id,kids])=>[id,kids.map(n=>n.id)]);
 for(const mode of modes){const a=M.layout(tree,collapsed,mode),b=M.layout(M.tree(rows.slice().reverse(),'r'),new Set(['a1']),mode);assert.deepEqual(a,b);checkGeometry(tree,a);}
 assert.equal(JSON.stringify(rows),before);assert.deepEqual([...collapsed],['a1']);assert.deepEqual([...tree.children].map(([id,kids])=>[id,kids.map(n=>n.id)]),order);
});

test('every mode supports deep 256-level trees, 1000-node stars and wide subtrees without geometry overlap',()=>{
 const chain=Array.from({length:256},(_,i)=>({id:String(i),parent_id:i?String(i-1):'',position:0}));
 const star=Array.from({length:1000},(_,i)=>({id:String(i),parent_id:i?'0':'',position:i}));
 const wide=[{id:'0',parent_id:'',position:0}];for(let g=0;g<9;g++){wide.push({id:`g${g}`,parent_id:'0',position:g});for(let i=0;i<110;i++)wide.push({id:`g${g}-${i}`,parent_id:`g${g}`,position:i});}
 assert.equal(wide.length,1000);
 for(const rows of [chain,star,wide])for(const mode of modes){const tree=M.tree(rows,'0'),layout=M.layout(tree,new Set(),mode);assert.equal(layout.visible.length,rows.length);checkGeometry(tree,layout);}
});

test('leftmost folded branch keeps its outward toggle fully inside the bilateral viewport',()=>{
 const rows=[...nodes,{id:'left-deep',parent_id:'b',position:0}],tree=M.tree(rows,'r');
 const layout=M.layout(tree,new Set(['a','b']),'bilateral'),control=layout.togglePositions.get('b');
 assert.equal(control.x-17,36);assert.ok(control.x<layout.positions.get('b').x);assert.ok(!layout.positions.has('left-deep'));
 checkGeometry(tree,layout);
});

test('every mode reserves controls on folded branches and reveals the exact ancestor path',()=>{
 const tree=M.tree(nodes,'r'),folded=new Set(['r','a','a1']);
 for(const mode of modes){
  const closed=M.layout(tree,folded,mode);assert.deepEqual(closed.visible.map(n=>n.id),['r']);assert.equal(closed.edgePaths.size,0);assert.equal(closed.togglePositions.size,1);checkGeometry(tree,closed);
  assert.equal(M.visibleAncestor(tree,closed,'deep'),'r');
  const revealed=M.reveal(tree,folded,'a1'),view=M.layout(tree,revealed,mode);
  assert.deepEqual(view.visible.map(n=>n.id),['r','a','a1','a2','b']);assert.ok(view.togglePositions.has('a1'));assert.ok(!view.positions.has('deep'));assert.equal(M.visibleAncestor(tree,view,'deep'),'a1');checkGeometry(tree,view);
  const single=M.tree([nodes[0]],'r'),one=M.layout(single,new Set(),mode);assert.equal(one.togglePositions.size,0);checkGeometry(single,one);
 }
});
