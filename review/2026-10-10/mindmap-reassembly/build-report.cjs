// Standalone, dependency-free rendering of the release's authored Markdown.
const fs=require('node:fs'),path=require('node:path'),{pathToFileURL}=require('node:url');
const source=path.resolve(__dirname,'../../../docs/MINDMAP_REASSEMBLY_RU.md');
const md=fs.readFileSync(source,'utf8'),lines=md.split(/\r?\n/);
const escape=s=>String(s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
function inline(s){
  const tokens=[];
  s=s.replace(/`([^`]+)`|\[([^\]]+)\]\(([^)]+)\)/g,(_,code,label,href)=>{
    let html;
    if(code)html='<code>'+escape(code)+'</code>';
    else {const url=/^https?:/.test(href)?href:pathToFileURL(path.resolve(path.dirname(source),href)).href;html='<a href="'+escape(url)+'">'+escape(label)+'</a>';}
    tokens.push(html);return '\u0000'+(tokens.length-1)+'\u0000';
  });
  return escape(s).replace(/\*\*([^*]+)\*\*/g,'<strong>$1</strong>').replace(/\u0000(\d+)\u0000/g,(_,i)=>tokens[Number(i)]);
}
const out=[];
for(let i=0;i<lines.length;){
  let line=lines[i];if(!line.trim()){i++;continue;}
  const h=line.match(/^(#{1,3}) (.*)$/);
  if(h){out.push('<h'+h[1].length+'>'+inline(h[2])+'</h'+h[1].length+'>');i++;continue;}
  if(line.startsWith('|')){
    const rows=[];while(i<lines.length&&lines[i].startsWith('|'))rows.push(lines[i++].split('|').slice(1,-1).map(x=>x.trim()));
    out.push('<div class="table"><table><thead><tr>'+rows[0].map(x=>'<th>'+inline(x)+'</th>').join('')+'</tr></thead><tbody>'+rows.slice(2).map(row=>'<tr>'+row.map(x=>'<td>'+inline(x)+'</td>').join('')+'</tr>').join('')+'</tbody></table></div>');continue;
  }
  if(/^(?:- |\d+\. )/.test(line)){
    const ordered=/^\d/.test(line),items=[];
    while(i<lines.length&&/^(?:- |\d+\. )/.test(lines[i])){
      let item=lines[i++].replace(/^(?:- |\d+\. )/,'');
      while(i<lines.length&&/^ {2,}\S/.test(lines[i]))item+=' '+lines[i++].trim();
      items.push('<li>'+inline(item)+'</li>');
    }
    const tag=ordered?'ol':'ul';out.push('<'+tag+'>'+items.join('')+'</'+tag+'>');continue;
  }
  const paragraph=[];
  while(i<lines.length&&lines[i].trim()&&!/^(?:#|\||- |\d+\. )/.test(lines[i]))paragraph.push(lines[i++].trim());
  out.push('<p>'+inline(paragraph.join(' '))+'</p>');
}
const html='<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Восстановление карты СП10 — 1.72.0-alpha.11</title><style>'+`
:root{font:17px/1.65 system-ui,"Segoe UI",sans-serif;color:#203832;background:#eff4f1}*{box-sizing:border-box}body{margin:0}main{max-width:1120px;margin:36px auto;background:#fff;padding:42px 48px;border:1px solid #d6e2dc;border-radius:16px}h1{font-size:35px;line-height:1.2;letter-spacing:-.8px;color:#145744}h2{margin:44px 0 18px;font-size:25px;border-bottom:2px solid #d6e2dc;padding-bottom:10px}p{max-width:90ch}a{color:#08684e;text-underline-offset:3px}code{background:#edf3f0;padding:2px 5px;border-radius:4px;overflow-wrap:anywhere}li{margin:10px 0}.table{overflow:auto}table{width:100%;border-collapse:collapse;font-size:15px}th,td{padding:13px;text-align:left;vertical-align:top;border:1px solid #d6e2dc}th{background:#eaf3ee}tr:nth-child(even){background:#f8faf9}.meta{color:#536e61;font-size:14px}footer{margin-top:35px;padding-top:20px;border-top:1px solid #d6e2dc}@media(max-width:720px){main{margin:0;padding:24px 18px;border:0;border-radius:0}h1{font-size:28px}td,th{min-width:180px}}@media print{body{background:white}main{border:0;margin:0;padding:0;max-width:none}a{color:inherit}h2{break-after:avoid}tr{break-inside:avoid}}
`+'</style></head><body><main><div class="meta">mem-tool · Исправление и проверка на документе · 10.10.2026</div>'+out.join('\n')+'<footer><a href="'+escape(pathToFileURL(source).href)+'">Исходный отчёт Markdown</a></footer></main></body></html>';
fs.writeFileSync(path.join(__dirname,'REPORT_RU.html'),html,'utf8');
console.log('Report written; '+out.length+' blocks; '+Buffer.byteLength(html)+' bytes.');
