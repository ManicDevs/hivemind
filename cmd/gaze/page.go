package main

import (
	"io"
	"net/http"
	"strings"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/worldmap"
)

// page serves the live SVG topology: real coastlines, great-circle
// links, a day/night terminator, nodes at real city coordinates. The
// SVG is built once per viewport size; each /state poll only patches
// the dynamic layer in place (keyed by node name) with CSS transitions
// — no innerHTML teardown, no flash, no flicker.

const pageTmpl = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>hivemind gaze</title>
<style>
  body{margin:0;background:#0a0e1a;color:#cfe3ff;font:14px/1.4 ui-monospace,Menlo,Consolas,monospace}
  header{padding:10px 16px;border-bottom:1px solid #1c2844;display:flex;gap:18px;align-items:center;flex-wrap:wrap}
  header b{color:#8ad0ff}
  .k{color:#5f7aa8}.v{color:#cfe3ff}
  #wrap{display:flex;height:calc(100vh - 52px)}
  #graph{flex:1 1 auto;min-width:0;position:relative}
  #side{width:340px;border-left:1px solid #1c2844;overflow-y:auto;padding:10px 12px}
  h3{margin:14px 0 6px;color:#8ad0ff;font-size:13px;letter-spacing:1px;text-transform:uppercase}
  table{width:100%;border-collapse:collapse}
  td,th{padding:2px 4px;text-align:left;border-bottom:1px solid #141d33}
  th{color:#5f7aa8;font-weight:normal}
  .ev{font-size:12px;border-left:2px solid #2a3a5c;padding:2px 6px;margin:3px 0}
  .ev .t{color:#5f7aa8}.ev .k{color:#ffd479}.ev .s{color:#7ee2a8}
  input,button{background:#0e1526;color:#cfe3ff;border:1px solid #2a3a5c;padding:4px 8px;border-radius:4px;font:inherit}
  button{cursor:pointer}button:hover{background:#15203a}
  #tabs{display:flex;gap:6px}
  #tabs .tab{background:#0e1526;border:1px solid #2a3a5c;padding:4px 12px;border-radius:14px;color:#5f7aa8}
  #tabs .tab.on{color:#ffd479;border-color:#ffd479}
  #viewtoggle{display:flex;gap:4px;margin-left:8px}
  #viewtoggle .vt{background:#0e1526;border:1px solid #2a3a5c;padding:4px 10px;border-radius:14px;color:#5f7aa8;font-size:12px;cursor:pointer}
  #viewtoggle .vt.on{color:#8ad0ff;border-color:#8ad0ff}
  #globe,#globeLabels{display:none;position:absolute;left:0;top:0;touch-action:none;cursor:grab}
  #globe.dragging,#globeLabels.dragging{cursor:grabbing}
  #globeLabels{pointer-events:none}
  body.view3d #graph > svg{display:none}
  body.view3d #globe,body.view3d #globeLabels{display:block}
  #notebar{padding:6px 16px;font-size:12px;color:#5f7aa8;border-bottom:1px solid #141d33}
  #toast{position:fixed;bottom:14px;left:50%;transform:translateX(-50%);background:#101a30;border:1px solid #2a3a5c;padding:6px 14px;border-radius:20px;display:none}
  #cmdbar{padding:8px 16px;border-bottom:1px solid #1c2844;display:flex;gap:14px;align-items:center;flex-wrap:wrap;background:#0d1322}
  #cmdbar .serverless{color:#5f7aa8;font-size:12px;max-width:560px;line-height:1.5}
  #cmdbar .pushlabel{color:#8ad0ff;font-size:12px;letter-spacing:1px;text-transform:uppercase}
  #cmdbar input{width:120px}
  #cmdbar input#cpayload{width:220px}
  .meta{color:#ff8f8f;font-size:12px;margin-top:12px}
  #hud{position:absolute;left:12px;bottom:10px;font-size:10px;color:#5f7aa8;pointer-events:none;line-height:1.6;z-index:5}
  #legend{position:absolute;right:12px;bottom:10px;font-size:10px;color:#5f7aa8;pointer-events:none;text-align:right;line-height:1.7;z-index:5}
  @keyframes hmpulse{0%,100%{opacity:1}50%{opacity:.5}}
  @keyframes hmflow{to{stroke-dashoffset:-40}}
  .hmlive{animation:hmpulse 2.4s ease-in-out infinite}
  .linkflow{animation:hmflow 1.4s linear infinite}
  .hmnode{cursor:default;transition:opacity .45s ease;pointer-events:auto}
  .hmnode .dot{transition:cx .7s cubic-bezier(.22,1,.36,1),cy .7s cubic-bezier(.22,1,.36,1),r .35s ease,fill .35s ease,stroke .35s ease}
  .hmnode:hover .dot{filter:brightness(1.7)}
  .hmnode:hover .lab{fill:#fff}
  .lab{transition:opacity .45s ease}
</style>
</head>
<body>
<header>
  <b>hivemind gaze</b>
  <nav id="tabs">
    <button class="tab on" id="tab-live" onclick="setTab('live')">live</button>
    <button class="tab" id="tab-replay" onclick="setTab('replay')">recorded run</button>
  </nav>
  <nav id="viewtoggle">
    <button class="vt on" id="vt-2d" onclick="setView('2d')">2D map</button>
    <button class="vt" id="vt-3d" onclick="setView('3d')">3D globe</button>
  </nav>
  <span><span class="k">node</span> <span class="v" id="node">…</span></span>
  <span><span class="k">uptime</span> <span class="v" id="uptime">…</span></span>
  <span><span class="k">frames</span> <span class="v" id="frames">…</span></span>
  <span><span class="k">links</span> <span class="v" id="links">…</span></span>
  <span><span class="k">hour key</span> <span class="v" id="hourkey">…</span></span>
  <span><span class="k">rotates in</span> <span class="v" id="rotates">…</span></span>
</header>
<div id="notebar" class="k"></div>
<div id="cmdbar">
  <div class="serverless">Serverless by construction: no registry, no hub. gaze joins as an equal node and paints what the frames say. Masters are just capable peers — any node is a full entry point.</div>
  <span class="pushlabel">push a command to the mesh broadcast</span>
  <input id="ckind" value="thought" placeholder="kind">
  <input id="cpayload" placeholder="payload">
  <button onclick="pushCommand()">broadcast</button>
</div>
<div id="wrap">
  <div id="graph">
    <canvas id="globe"></canvas>
    <canvas id="globeLabels"></canvas>
    <div id="hud"></div>
    <div id="legend">◉ master · ● peer · ┄ great-circle link · ╍ day/night line · 3D: drag orbit · wheel zoom</div>
  </div>
  <div id="side">
    <h3>Relay (optional)</h3>
    <table>
      <tr><th>cloud</th><td id="relay">…</td></tr>
      <tr><th>url</th><td id="relayurl">…</td></tr>
    </table>
    <h3>Frame kinds</h3>
    <div id="kinds">…</div>
    <h3>Live stream</h3>
    <div id="stream">…</div>
  </div>
</div>
<div id="toast"></div>
<script>
var svgns="http://www.w3.org/2000/svg";
var contColors=["#4fc3f7","#ffb74d","#81c784","#ba68c8","#ef5350","#ffd54f","#4db6ac"];
var continentOrder=["eu","as","af","na","sa","oc","an"];
var mode="live";

var cityLL={
"rotterdam":[4.48,51.92],"london":[-0.13,51.51],"frankfurt":[8.68,50.11],
"paris":[2.35,48.86],"amsterdam":[4.90,52.37],"berlin":[13.40,52.52],
"new-york":[-74.01,40.71],"chicago":[-87.63,41.88],"san-francisco":[-122.42,37.77],
"toronto":[-79.38,43.65],"dallas":[-96.80,32.78],"seattle":[-122.33,47.61],
"tokyo":[139.69,35.68],"singapore":[103.82,1.35],"seoul":[126.98,37.57],
"mumbai":[72.88,19.08],"dubai":[55.27,25.20],"jakarta":[106.85,-6.21],
"sao-paulo":[-46.63,-23.55],"buenos-aires":[-58.38,-34.60],"lima":[-77.04,-12.05],
"bogota":[-74.07,4.71],"johannesburg":[28.05,-26.20],"lagos":[3.38,6.52],
"cairo":[31.24,30.04],"nairobi":[36.82,-1.29],"sydney":[151.21,-33.87],
"auckland":[174.76,-36.85],"melbourne":[144.96,-37.81],"brisbane":[153.03,-27.47],
"mcmurdo":[166.67,-77.85],"amundsen-scott":[0,-90]
};
var contFallback={eu:[10,50],na:[-95,40],as:[100,35],sa:[-60,-15],af:[20,5],oc:[145,-30],an:[0,-80]};
var contLabels={eu:[15,55],na:[-100,55],as:[105,50],sa:[-60,-30],af:[20,15],oc:[140,-25],an:[0,-75]};

// Simplified coastlines: flat [lon,lat,...] rings, same geometry as
// the server-side world.svg engine.
var COAST=/*COAST_JSON*/[];

var mapSvg=null,gStatic=null,gDyn=null,gTerm=null,gLinks=null,gLabels=null,gNodes=null,lastW=0,lastH=0;
var nodeEls={},linkEls={},labelEls={},gazeEl=null,termEl=null,subEl=null;
var viewMode="2d";
var gLastState=null;
// WebGL orbit state: yaw/pitch radians, dist = camera distance, inertial spin.
var gl=null,glProg=null,glProgStar=null,glProgLine=null,glSphere=null,glArc=null,glPts=null,glStars=null,glTex=null;
var gYaw=0.7,gPitch=0.35,gDist=2.85,gDrag=false,gPX=0,gPY=0;
var gVX=0,gVY=0,gSpin=true,gReady=false,gRAF=0,gOverlay=null,gOctx=null;
var gEarthCanvas=null;

function setTab(m){
  mode=m;
  document.getElementById("tab-live").className="tab"+(m==="live"?" on":"");
  document.getElementById("tab-replay").className="tab"+(m==="replay"?" on":"");
  load();
}

function setView(v){
  viewMode=v;
  document.getElementById("vt-2d").className="vt"+(v==="2d"?" on":"");
  document.getElementById("vt-3d").className="vt"+(v==="3d"?" on":"");
  document.body.className=(v==="3d")?"view3d":"";
  var legend=document.getElementById("legend");
  if(v==="3d"){
    legend.textContent="◉ master · ● peer · ┄ great-circle arc · drag orbit · wheel zoom · double-click reset";
    ensureGlobe();
    if(!gRAF)gRAF=requestAnimationFrame(globeFrame);
  }else{
    legend.textContent="◉ master · ● peer · ┄ great-circle link · ╍ day/night line · 3D: drag orbit · wheel zoom";
    if(gRAF){cancelAnimationFrame(gRAF);gRAF=0;}
    lastW=0;
  }
  load();
}

function load(){
  var q=(mode==="replay")?"?mode=replay":"";
  fetch("/state"+q,{cache:"no-store"}).then(function(r){return r.json()}).then(render).catch(function(e){});
}

function render(s){
  var g=s.gaze;
  document.getElementById("node").textContent=g.node;
  document.getElementById("uptime").textContent=g.uptime;
  document.getElementById("frames").textContent=g.frames_total;
  document.getElementById("links").textContent=g.linked_peers;
  document.getElementById("hourkey").textContent=(g.hour_key||"…").slice(0,16);
  document.getElementById("rotates").textContent=g.hour_rotates;
  document.getElementById("relay").textContent=g.relay_on?"on":"off (pure mesh)";
  document.getElementById("relayurl").textContent=g.relay_url||"—";
  document.getElementById("notebar").textContent=g.note||"";

  var kinds=g.by_kind||{};
  var kh="";
  for(var k in kinds){kh=kh+'<div class="ev"><span class="k">'+k+'</span> &middot; '+kinds[k]+'</div>'}
  document.getElementById("kinds").innerHTML=kh||"<div class='ev'>silent</div>";

  var ev=(s.events||[]).slice().reverse().slice(0,10);
  var es="";
  for(var i in ev){var e=ev[i];es=es+'<div class="ev"><span class="t">'+e.at.slice(11,19)+'</span> <span class="k">'+e.kind+'</span> <span class="s">'+short(e.sender)+'</span> '+esc(e.payload||'')+'</div>'}
  document.getElementById("stream").innerHTML=es;

  document.getElementById("hud").innerHTML=(g.now||"")+" · "+g.node+" · frames "+g.frames_total;

  gLastState=s;
  if(viewMode==="3d"){
    ensureGlobe();
    if(!gRAF)gRAF=requestAnimationFrame(globeFrame);
  }else{
    drawGraph(s,g);
  }
}

function project(lon,lat,w,h){
  var pad=0.04;
  var nx=(lon+180)/360, ny=(90-lat)/180;
  return {x:w*(pad+nx*(1-2*pad)), y:h*(pad+ny*(1-2*pad))};
}

function cityOf(name){
  var parts=String(name||"").split("-");
  if(parts.length<3)return "";
  return parts.slice(2).join("-");
}

function nodeLL(name,cont){
  var c=cityOf(name);
  if(c&&cityLL[c])return cityLL[c];
  if(contFallback[cont])return contFallback[cont];
  return [0,0];
}

function contIndex(c){
  var i=continentOrder.indexOf(c);
  return i<0?0:i;
}

function short(n){return (n||"").length>16?n.slice(0,16)+"…":n}
function esc(t){return String(t).replace(/[&<>]/g,function(c){return {"&":"&amp;","<":"&lt;",">":"&gt;"}[c]})}

function el(tag,attrs,parent){
  var e=document.createElementNS(svgns,tag);
  if(attrs)for(var k in attrs)e.setAttribute(k,attrs[k]);
  if(parent)parent.appendChild(e);
  return e;
}

function ensureSVG(){
  var host=document.getElementById("graph");
  var w=host.clientWidth,h=host.clientHeight;
  if(w<10||h<10)return false;
  if(mapSvg&&w===lastW&&h===lastH)return true;

  // Size changed or first build: rebuild the shell once.
  if(mapSvg&&mapSvg.parentNode)mapSvg.parentNode.removeChild(mapSvg);
  nodeEls={};linkEls={};labelEls={};gazeEl=null;termEl=null;subEl=null;
  lastW=w;lastH=h;

  mapSvg=el("svg",{width:w,height:h,viewBox:"0 0 "+w+" "+h},host);
  mapSvg.style.position="absolute";
  mapSvg.style.left="0";mapSvg.style.top="0";
  mapSvg.style.pointerEvents="none";
  mapSvg.setAttribute("font-family","ui-monospace,Menlo,Consolas,monospace");
  el("rect",{x:0,y:0,width:w,height:h,fill:"#0a0e1a"},mapSvg);
  gStatic=el("g",{id:"gstatic"},mapSvg);
  // Fixed stacking order inside the dynamic layer — groups are never
  // reordered, so nodes always sit above links without DOM moves.
  gDyn=el("g",{id:"gdyn"},mapSvg);
  gTerm=el("g",{id:"gterm"},gDyn);
  gLinks=el("g",{id:"glinks"},gDyn);
  gLabels=el("g",{id:"glabels"},gDyn);
  gNodes=el("g",{id:"gnodes"},gDyn);
  drawStatic(w,h);
  return true;
}

function drawStatic(w,h){
  while(gStatic.firstChild)gStatic.removeChild(gStatic.firstChild);

  // Graticule every 30°.
  for(var lat=-60;lat<=60;lat+=30){
    var y=project(0,lat,w,h).y;
    el("line",{x1:0,y1:y,x2:w,y2:y,stroke:"#131c30","stroke-width":0.8},gStatic);
    var lab=lat>0?(lat+"N"):(lat<0?(-lat+"S"):"0°");
    el("text",{x:4,y:y-3,fill:"#2a3a5c","font-size":9},gStatic).textContent=lab;
  }
  var ey=project(0,0,w,h).y;
  el("line",{x1:0,y1:ey,x2:w,y2:ey,stroke:"#1e2d4a","stroke-width":1.2},gStatic);
  for(var lon=-150;lon<=150;lon+=30){
    var x=project(lon,0,w,h).x;
    el("line",{x1:x,y1:0,x2:x,y2:h,stroke:"#131c30","stroke-width":0.8},gStatic);
    var ll=lon>0?(lon+"E"):(lon<0?(-lon+"W"):"0°");
    el("text",{x:x,y:h-6,fill:"#2a3a5c","font-size":9,"text-anchor":"middle"},gStatic).textContent=ll;
  }
  var pmx=project(0,0,w,h).x;
  el("line",{x1:pmx,y1:0,x2:pmx,y2:h,stroke:"#1e2d4a","stroke-width":1.2},gStatic);

  // Coastlines.
  for(var i=0;i<COAST.length;i++){
    var flat=COAST[i],d="";
    for(var j=0;j<flat.length;j+=2){
      var p=project(flat[j],flat[j+1],w,h);
      d+=(j===0?"M":"L")+p.x.toFixed(1)+","+p.y.toFixed(1);
    }
    d+="Z";
    el("path",{d:d,fill:"#111a2e",stroke:"#243b5e","stroke-width":1,"stroke-linejoin":"round"},gStatic);
  }
}

function solarPosition(date){
  var hour=date.getUTCHours()+date.getUTCMinutes()/60+date.getUTCSeconds()/3600;
  var start=Date.UTC(date.getUTCFullYear(),0,0);
  var day=Math.floor((date.getTime()-start)/86400000);
  var gamma=2*Math.PI/365*(day-1+hour/24);
  var decl=0.006918-0.399912*Math.cos(gamma)+0.070257*Math.sin(gamma)
    -0.006758*Math.cos(2*gamma)+0.000907*Math.sin(2*gamma)
    -0.002697*Math.cos(3*gamma)+0.00148*Math.sin(3*gamma);
  decl*=180/Math.PI;
  var eq=229.18*(0.000075+0.001868*Math.cos(gamma)-0.032077*Math.sin(gamma)
    -0.014615*Math.cos(2*gamma)-0.040849*Math.sin(2*gamma));
  var slon=180-(hour*15+eq);
  while(slon>180)slon-=360;
  while(slon<-180)slon+=360;
  return {lon:slon,decl:decl};
}

function terminatorPath(slon,decl,w,h){
  var d=decl*Math.PI/180;
  if(Math.abs(d)<1e-6)d=1e-6;
  var pts=[];
  for(var lon=-180;lon<=180;lon+=3){
    var dlon=(lon-slon)*Math.PI/180;
    var phi=Math.atan2(-Math.cos(dlon),Math.tan(d))*180/Math.PI;
    pts.push([lon,phi]);
  }
  var dstr="",i;
  for(i=0;i<pts.length;i++){
    var p=project(pts[i][0],pts[i][1],w,h);
    dstr+=(i===0?"M":"L")+p.x.toFixed(1)+","+p.y.toFixed(1);
  }
  // Night shading: close along the pole on the anti-subsolar side.
  var antiLon=slon+180; if(antiLon>180)antiLon-=360;
  var antiLat=-decl;
  var termAtAnti=Math.atan2(-Math.cos((antiLon-slon)*Math.PI/180),Math.tan(d))*180/Math.PI;
  var closeNorth=antiLat>termAtAnti;
  var edgeLat=closeNorth?90:-90;
  var e1=project(180,edgeLat,w,h), e2=project(-180,edgeLat,w,h);
  var night=dstr+"L"+w+","+e1.y.toFixed(1)+"L0,"+e2.y.toFixed(1)+"Z";
  return {line:dstr,night:night,pts:pts};
}

function greatCirclePath(fromLon,fromLat,w,h,segs){
  // Great-circle from node to gaze at (0,0): slerp then project.
  var toXYZ=function(lon,lat){
    var la=lat*Math.PI/180, lo=lon*Math.PI/180;
    return [Math.cos(la)*Math.cos(lo),Math.cos(la)*Math.sin(lo),Math.sin(la)];
  };
  var a=toXYZ(fromLon,fromLat), b=toXYZ(0,0);
  var dot=a[0]*b[0]+a[1]*b[1]+a[2]*b[2];
  if(dot>1)dot=1; if(dot<-1)dot=-1;
  var om=Math.acos(dot);
  if(om<1e-9)return "M0,0";
  var sinOm=Math.sin(om), d="", prevLon=null;
  for(var i=0;i<=segs;i++){
    var f=i/segs;
    var s1=Math.sin((1-f)*om)/sinOm, s2=Math.sin(f*om)/sinOm;
    var x=s1*a[0]+s2*b[0], y=s1*a[1]+s2*b[1], z=s1*a[2]+s2*b[2];
    var lat=Math.atan2(z,Math.sqrt(x*x+y*y))*180/Math.PI;
    var lon=Math.atan2(y,x)*180/Math.PI;
    if(prevLon!==null){
      while(lon-prevLon>180)lon-=360;
      while(lon-prevLon<-180)lon+=360;
    }
    prevLon=lon;
    var p=project(lon,lat,w,h);
    d+=(i===0?"M":"L")+p.x.toFixed(1)+","+p.y.toFixed(1);
  }
  var g=project(0,0,w,h);
  d+="L"+g.x.toFixed(1)+","+g.y.toFixed(1);
  return d;
}

function ensureGaze(w,h){
  if(gazeEl)return gazeEl;
  var g=el("g",{"class":"hmnode"},gNodes);
  el("title",null,g).textContent="gaze · observer · equal peer";
  el("circle",{"class":"dot",r:10,fill:"none",stroke:"#ffd479","stroke-width":2},g);
  var t=el("text",{"class":"lab",fill:"#ffd479","font-size":10,"text-anchor":"middle"},g);
  t.textContent="gaze";
  gazeEl={g:g,c:g.firstChild.nextSibling,t:t};
  return gazeEl;
}

function ensureTerm(){
  if(termEl)return termEl;
  var night=el("path",{fill:"#040710","fill-opacity":0.45},gTerm);
  var line=el("path",{fill:"none",stroke:"#ffd479","stroke-width":1.4,"stroke-opacity":0.7,"stroke-dasharray":"6 4"},gTerm);
  subEl=el("circle",{r:5,fill:"#ffd479","fill-opacity":0.9},gTerm);
  var halo=el("circle",{r:9,fill:"none",stroke:"#ffd479","stroke-width":1,"stroke-opacity":0.5},gTerm);
  termEl={night:night,line:line,sub:subEl,halo:halo};
  return termEl;
}

function drawGraph(s,g){
  if(!ensureSVG())return;
  var w=lastW,h=lastH;
  var nodes=s.nodes||[];
  var links=g.link_names||[];

  // --- terminator (recomputed each tick, attribute patch only) ---
  var sp=solarPosition(new Date());
  var tp=terminatorPath(sp.lon,sp.decl,w,h);
  var te=ensureTerm();
  te.night.setAttribute("d",tp.night);
  te.line.setAttribute("d",tp.line);
  var spPos=project(sp.lon,sp.decl,w,h);
  te.sub.setAttribute("cx",spPos.x);te.sub.setAttribute("cy",spPos.y);
  te.halo.setAttribute("cx",spPos.x);te.halo.setAttribute("cy",spPos.y);

  // --- gaze hub ---
  var ge=ensureGaze(w,h);
  var gc=project(0,0,w,h);
  ge.c.setAttribute("cx",gc.x);ge.c.setAttribute("cy",gc.y);
  ge.t.setAttribute("x",gc.x);ge.t.setAttribute("y",gc.y+24);

  // --- project + index nodes ---
  var seen={},pos={};
  for(var i=0;i<nodes.length;i++){
    var n=nodes[i];
    seen[n.name]=true;
    var ll=nodeLL(n.name,n.continent);
    var p=project(ll[0],ll[1],w,h);
    pos[n.name]={x:p.x,y:p.y,n:n};
  }

  // --- links: keyed by target, path d patched in place ---
  var linkSeen={};
  for(var li=0;li<links.length;li++){
    var lname=links[li];
    if(!pos[lname])continue;
    linkSeen[lname]=true;
    var pt=pos[lname];
    var ll2=nodeLL(lname,pt.n.continent);
    var d=greatCirclePath(ll2[0],ll2[1],w,h,20);
    var le=linkEls[lname];
    if(!le){
      le=el("path",{"class":"linkflow",fill:"none",stroke:"#3a6aaf","stroke-width":1.3,"stroke-opacity":0.75,"stroke-dasharray":"5 5"},gLinks);
      linkEls[lname]=le;
    }
    le.setAttribute("d",d);
  }
  for(var lk in linkEls){
    if(!linkSeen[lk]){
      var dead=linkEls[lk];
      if(dead.parentNode)dead.parentNode.removeChild(dead);
      delete linkEls[lk];
    }
  }

  // --- continent labels (patched) ---
  var contSeen={};
  var counts={};
  for(var ci=0;ci<nodes.length;ci++){
    var c=nodes[ci].continent;
    counts[c]=(counts[c]||0)+1;
  }
  for(var c2 in contLabels){
    if(!counts[c2])continue;
    contSeen[c2]=true;
    var cl=project(contLabels[c2][0],contLabels[c2][1],w,h);
    var le2=labelEls[c2];
    if(!le2){
      var lg=el("g",null,gLabels);
      var nameT=el("text",{"text-anchor":"middle","font-size":14,"font-weight":"bold","letter-spacing":2,opacity:0.9},lg);
      var cntT=el("text",{"text-anchor":"middle","font-size":9,opacity:0.6},lg);
      le2=labelEls[c2]={g:lg,name:nameT,cnt:cntT};
    }
    var col=contColors[contIndex(c2)%contColors.length];
    le2.name.setAttribute("x",cl.x);le2.name.setAttribute("y",cl.y-18);
    le2.name.setAttribute("fill",col);le2.name.textContent=c2.toUpperCase();
    le2.cnt.setAttribute("x",cl.x);le2.cnt.setAttribute("y",cl.y-6);
    le2.cnt.setAttribute("fill",col);le2.cnt.textContent=counts[c2]+" nodes";
  }
  for(var cl2 in labelEls){
    if(!contSeen[cl2]){
      var lg2=labelEls[cl2].g;
      if(lg2.parentNode)lg2.parentNode.removeChild(lg2);
      delete labelEls[cl2];
    }
  }

  // --- nodes: keyed diff, CSS transitions on cx/cy/r/fill ---
  var nSeen={};
  for(var ni=0;ni<nodes.length;ni++){
    var node=nodes[ni];
    nSeen[node.name]=true;
    var pp=pos[node.name];
    var isMasterNode=/master|super/.test(node.name.toLowerCase());
    var isGazeNode=node.name.toLowerCase().indexOf("gaze")===0;
    var role=isGazeNode?"gaze":(isMasterNode?"master":"peer");
    var r=role==="master"?12:(role==="gaze"?8:7);
    var col2=contColors[contIndex(node.continent)%contColors.length];
    var fill=node.alive?col2:"#3d5a80";
    var stroke=node.alive?"#ffffff":"#8eb1d9";
    var ne=nodeEls[node.name];
    if(!ne){
      var ng=el("g",{"class":"hmnode",opacity:0},gNodes);
      var title=el("title",null,ng);
      var dot=el("circle",{"class":"dot",cx:pp.x,cy:pp.y,r:r,fill:fill,stroke:stroke,"stroke-width":1.5},ng);
      var lab=el("text",{"class":"lab",x:pp.x,y:pp.y+r+11,"text-anchor":"middle",fill:"#cfe3ff","font-size":9},ng);
      lab.textContent=short(node.name);
      nodeEls[node.name]={g:ng,title:title,dot:dot,lab:lab};
      ne=nodeEls[node.name];
      // Fade in on the next frame so appearance is smooth.
      requestAnimationFrame(function(gEl){return function(){gEl.setAttribute("opacity",1)}}(ng));
    }
    ne.title.textContent=node.name+" · "+role+" · "+String(node.continent||"?").toUpperCase()
      +" · "+(node.alive?"alive":"resting")+" · lives="+node.lives+" · fitness="+(node.fitness||0).toFixed(2);
    ne.dot.setAttribute("cx",pp.x);
    ne.dot.setAttribute("cy",pp.y);
    ne.dot.setAttribute("r",r);
    ne.dot.setAttribute("fill",fill);
    ne.dot.setAttribute("stroke",stroke);
    ne.lab.setAttribute("x",pp.x);
    ne.lab.setAttribute("y",pp.y+r+11);
    if(node.alive){
      ne.dot.setAttribute("class","dot hmlive");
      ne.dot.style.setProperty("--r",r);
    }else{
      ne.dot.setAttribute("class","dot");
      ne.dot.style.removeProperty("--r");
    }
  }
  for(var nm in nodeEls){
    if(!nSeen[nm]){
      (function(pair){
        pair.g.setAttribute("opacity",0);
        setTimeout(function(){
          if(pair.g.parentNode)pair.g.parentNode.removeChild(pair.g);
        },480);
      })(nodeEls[nm]);
      delete nodeEls[nm];
    }
  }
}

function pushCommand(){
  var kind=document.getElementById("ckind").value||"thought";
  var payload=document.getElementById("cpayload").value;
  document.getElementById("cpayload").value="";
  fetch("/command",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({kind:kind,payload:payload})}).then(function(r){return r.json()}).then(function(j){
    var t=document.getElementById("toast");
    t.textContent=j.accepted?("broadcast: "+j.kind+" / "+j.payload):("rejected: "+j.kind);
    t.style.display="block";setTimeout(function(){t.style.display="none"},2500);
  });
}

// ─── real 3D interactive globe (WebGL) ───────────────────────────────
// Sphere mesh + equirectangular earth baked from COAST, lambert sun,
// ocean specular, fresnel atmosphere, great-circle arcs, inertial orbit.
// No CDN: raw WebGL1 inside the page string (no backticks in JS).

function llToXYZ(lon,lat,r){
  r=r||1;
  var la=lat*Math.PI/180,lo=lon*Math.PI/180;
  var c=Math.cos(la);
  return [r*c*Math.cos(lo),r*c*Math.sin(lo),r*Math.sin(la)];
}

function m4Ident(){return [1,0,0,0, 0,1,0,0, 0,0,1,0, 0,0,0,1];}
function m4Mul(a,b){
  var o=new Array(16);
  for(var c=0;c<4;c++)for(var r=0;r<4;r++){
    o[c*4+r]=a[r]*b[c*4]+a[4+r]*b[c*4+1]+a[8+r]*b[c*4+2]+a[12+r]*b[c*4+3];
  }
  return o;
}
function m4Persp(fovy,asp,n,f){
  var t=1/Math.tan(fovy/2);
  return [t/asp,0,0,0, 0,t,0,0, 0,0,(f+n)/(n-f),-1, 0,0,2*f*n/(n-f),0];
}
function m4Trans(x,y,z){return [1,0,0,0, 0,1,0,0, 0,0,1,0, x,y,z,1];}
function m4RotY(a){
  var c=Math.cos(a),s=Math.sin(a);
  return [c,0,-s,0, 0,1,0,0, s,0,c,0, 0,0,0,1];
}
function m4RotX(a){
  var c=Math.cos(a),s=Math.sin(a);
  return [1,0,0,0, 0,c,s,0, 0,-s,c,0, 0,0,0,1];
}
function m4Apply(m,v){
  return [
    m[0]*v[0]+m[4]*v[1]+m[8]*v[2]+m[12]*v[3],
    m[1]*v[0]+m[5]*v[1]+m[9]*v[2]+m[13]*v[3],
    m[2]*v[0]+m[6]*v[1]+m[10]*v[2]+m[14]*v[3],
    m[3]*v[0]+m[7]*v[1]+m[11]*v[2]+m[15]*v[3]
  ];
}
function m4View(yaw,pitch,dist){
  // camera orbits: rotate world by -yaw/-pitch then pull back on Z
  var m=m4Mul(m4Trans(0,0,-dist), m4Mul(m4RotX(-pitch), m4RotY(-yaw)));
  return m;
}
function m4Model(){return m4Ident();}

function bakeEarthTexture(){
  if(gEarthCanvas)return gEarthCanvas;
  var w=2048,h=1024;
  var c=document.createElement("canvas");
  c.width=w;c.height=h;
  var x=c.getContext("2d");
  var ocean=x.createLinearGradient(0,0,0,h);
  ocean.addColorStop(0,"#0c1f38");
  ocean.addColorStop(0.45,"#0a1a30");
  ocean.addColorStop(1,"#081528");
  x.fillStyle=ocean;
  x.fillRect(0,0,w,h);
  // faint ocean depth bands
  x.globalAlpha=0.15;
  for(var lat=-80;lat<=80;lat+=20){
    var y=(90-lat)/180*h;
    x.fillStyle=lat%40===0?"#12304f":"#0e2742";
    x.fillRect(0,y-8,w,16);
  }
  x.globalAlpha=1;
  var lands=["#1e3a28","#24422c","#1c3524","#2a4a30","#224028","#1a3222","#2e5234"];
  for(var i=0;i<COAST.length;i++){
    var flat=COAST[i];
    x.beginPath();
    for(var j=0;j<flat.length;j+=2){
      var px=(flat[j]+180)/360*w;
      var py=(90-flat[j+1])/180*h;
      if(j===0)x.moveTo(px,py);else x.lineTo(px,py);
    }
    x.closePath();
    x.fillStyle=lands[i%lands.length];
    x.fill();
    x.strokeStyle="#3f7a55";
    x.lineWidth=1.5;
    x.stroke();
  }
  // graticule on texture (subtle)
  x.strokeStyle="rgba(80,120,170,0.18)";
  x.lineWidth=1;
  for(var lo=-180;lo<=180;lo+=30){
    var gx=(lo+180)/360*w;
    x.beginPath();x.moveTo(gx,0);x.lineTo(gx,h);x.stroke();
  }
  for(var la2=-60;la2<=60;la2+=30){
    var gy=(90-la2)/180*h;
    x.beginPath();x.moveTo(0,gy);x.lineTo(w,gy);x.stroke();
  }
  gEarthCanvas=c;
  return c;
}

function buildSphere(seg){
  var pos=[],nor=[],uv=[],idx=[];
  for(var y=0;y<=seg;y++){
    var v=y/seg, phi=v*Math.PI;
    for(var x=0;x<=seg;x++){
      var u=x/seg, theta=u*2*Math.PI;
      // equirectangular: lon = theta-PI maps u=(lon+PI)/(2PI)=u
      var lat=90-v*180, lon=u*360-180;
      var p=llToXYZ(lon,lat,1);
      pos.push(p[0],p[1],p[2]);
      nor.push(p[0],p[1],p[2]);
      uv.push(u,v);
    }
  }
  var row=seg+1;
  for(var y2=0;y2<seg;y2++){
    for(var x2=0;x2<seg;x2++){
      var a=y2*row+x2, b=a+1, c=a+row, d=c+1;
      idx.push(a,c,b, b,c,d);
    }
  }
  return {pos:new Float32Array(pos),nor:new Float32Array(nor),uv:new Float32Array(uv),idx:new Uint16Array(idx),n:idx.length};
}

function buildStars(){
  var N=800, arr=new Float32Array(N*3);
  for(var i=0;i<N;i++){
    var u=Math.random()*2-1, t=Math.random()*Math.PI*2, s=Math.sqrt(1-u*u);
    arr[i*3]=Math.cos(t)*s*18;
    arr[i*3+1]=Math.sin(t)*s*18;
    arr[i*3+2]=u*18;
  }
  return arr;
}

function compile(g,type,src){
  var s=g.createShader(type);
  g.shaderSource(s,src);
  g.compileShader(s);
  if(!g.getShaderParameter(s,g.COMPILE_STATUS)){
    console.error(g.getShaderInfoLog(s),src);
    return null;
  }
  return s;
}
function link(g,vs,fs){
  var p=g.createProgram();
  g.attachShader(p,vs);
  g.attachShader(p,fs);
  g.linkProgram(p);
  if(!g.getProgramParameter(p,g.LINK_STATUS)){
    console.error(g.getProgramInfoLog(p));
    return null;
  }
  return p;
}

function ensureGlobe(){
  if(gReady)return true;
  var canvas=document.getElementById("globe");
  gOverlay=document.getElementById("globeLabels");
  if(!canvas)return false;
  gl=canvas.getContext("webgl",{antialias:true,alpha:false})||canvas.getContext("experimental-webgl",{antialias:true,alpha:false});
  if(!gl){
    document.getElementById("hud").textContent="WebGL unavailable — staying on 2D map";
    viewMode="2d";
    document.body.className="";
    document.getElementById("vt-2d").className="vt on";
    document.getElementById("vt-3d").className="vt";
    return false;
  }
  gOctx=gOverlay.getContext("2d");

  var vsSrc=[
    "attribute vec3 aPos;",
    "attribute vec3 aNor;",
    "attribute vec2 aUV;",
    "uniform mat4 uMVP;",
    "uniform mat4 uModel;",
    "varying vec3 vN;",
    "varying vec2 vUV;",
    "varying vec3 vW;",
    "void main(){",
    "  vUV=aUV;",
    "  mat3 nm=mat3(uModel);",
    "  vN=normalize(nm*aNor);",
    "  vec4 w=uModel*vec4(aPos,1.0);",
    "  vW=w.xyz;",
    "  gl_Position=uMVP*vec4(aPos,1.0);",
    "}"
  ].join("\n");
  var fsSrc=[
    "precision mediump float;",
    "uniform sampler2D uTex;",
    "uniform vec3 uSun;",
    "uniform vec3 uCam;",
    "varying vec3 vN;",
    "varying vec2 vUV;",
    "varying vec3 vW;",
    "void main(){",
    "  vec3 n=normalize(vN);",
    "  vec3 albedo=texture2D(uTex,vUV).rgb;",
    "  vec3 view=normalize(uCam-vW);",
    "  float ndl=dot(n,uSun);",
    "  float day=smoothstep(-0.12,0.28,ndl);",
    "  float diff=max(ndl,0.0);",
    "  vec3 col=albedo*(0.12+0.95*diff);",
    "  // ocean specular: darker texels are water",
    "  float ocean=1.0-smoothstep(0.18,0.32,albedo.g);",
    "  vec3 h=normalize(uSun+view);",
    "  col+=vec3(1.0,0.92,0.75)*pow(max(dot(n,h),0.0),48.0)*ocean*day*0.55;",
    "  // city glow on night land",
    "  float land=smoothstep(0.2,0.4,albedo.g);",
    "  col+=vec3(1.0,0.85,0.45)*land*(1.0-day)*0.08;",
    "  // fresnel atmosphere",
    "  float fres=pow(1.0-max(dot(n,view),0.0),2.6);",
    "  col+=vec3(0.28,0.55,1.0)*fres*0.65;",
    "  // slight ambient so night side isn't pure black",
    "  col+=albedo*0.04;",
    "  gl_FragColor=vec4(col,1.0);",
    "}"
  ].join("\n");

  glProg=link(gl,compile(gl,gl.VERTEX_SHADER,vsSrc),compile(gl,gl.FRAGMENT_SHADER,fsSrc));
  if(!glProg)return false;

  var sphere=buildSphere(72);
  glSphere={
    pos:buffer(gl,gl.ARRAY_BUFFER,sphere.pos),
    nor:buffer(gl,gl.ARRAY_BUFFER,sphere.nor),
    uv:buffer(gl,gl.ARRAY_BUFFER,sphere.uv),
    idx:buffer(gl,gl.ELEMENT_ARRAY_BUFFER,sphere.idx),
    n:sphere.n
  };

  // earth texture
  glTex=gl.createTexture();
  gl.bindTexture(gl.TEXTURE_2D,glTex);
  gl.texImage2D(gl.TEXTURE_2D,0,gl.RGBA,gl.RGBA,gl.UNSIGNED_BYTE,bakeEarthTexture());
  gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_WRAP_S,gl.REPEAT);
  gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_WRAP_T,gl.CLAMP_TO_EDGE);
  gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_MIN_FILTER,gl.LINEAR);
  gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_MAG_FILTER,gl.LINEAR);

  // arc + point dynamic buffers
  glArc={buf:gl.createBuffer(),max:4096};
  glPts={buf:gl.createBuffer(),max:256};
  glStars=buffer(gl,gl.ARRAY_BUFFER,buildStars());

  // simple star program
  var svs=["attribute vec3 aPos;","uniform mat4 uMVP;","void main(){gl_Position=uMVP*vec4(aPos,1.0);gl_PointSize=1.5;}"].join("\n");
  var sfs=["precision mediump float;","void main(){gl_FragColor=vec4(0.75,0.85,1.0,0.85);}"].join("\n");
  glProgStar=link(gl,compile(gl,gl.VERTEX_SHADER,svs),compile(gl,gl.FRAGMENT_SHADER,sfs));

  // line program for arcs / graticule-style links
  var lvs=["attribute vec3 aPos;","uniform mat4 uMVP;","void main(){gl_Position=uMVP*vec4(aPos,1.0);}"].join("\n");
  var lfs=["precision mediump float;","uniform vec4 uColor;","void main(){gl_FragColor=uColor;}"].join("\n");
  glProgLine=link(gl,compile(gl,gl.VERTEX_SHADER,lvs),compile(gl,gl.FRAGMENT_SHADER,lfs));

  bindOrbit(canvas);
  gReady=true;
  sizeGlobe();
  return true;
}

function buffer(gl,target,data){
  var b=gl.createBuffer();
  gl.bindBuffer(target,b);
  gl.bufferData(target,data,gl.STATIC_DRAW);
  return b;
}

function sizeGlobe(){
  if(!gl)return;
  var host=document.getElementById("graph");
  var w=host.clientWidth,h=host.clientHeight;
  if(w<10||h<10)return;
  var dpr=Math.min(window.devicePixelRatio||1,2);
  var canvas=document.getElementById("globe");
  canvas.style.width=w+"px";canvas.style.height=h+"px";
  canvas.width=Math.floor(w*dpr);canvas.height=Math.floor(h*dpr);
  gOverlay.style.width=w+"px";gOverlay.style.height=h+"px";
  gOverlay.width=Math.floor(w*dpr);gOverlay.height=Math.floor(h*dpr);
  gOctx.setTransform(dpr,0,0,dpr,0,0);
  canvas._w=w;canvas._h=h;canvas._dpr=dpr;
  gl.viewport(0,0,canvas.width,canvas.height);
}

function bindOrbit(canvas){
  canvas.addEventListener("pointerdown",function(e){
    gDrag=true;gSpin=false;gVX=0;gVY=0;gPX=e.clientX;gPY=e.clientY;
    canvas.classList.add("dragging");
    gOverlay.classList.add("dragging");
    canvas.setPointerCapture(e.pointerId);
  });
  canvas.addEventListener("pointermove",function(e){
    if(!gDrag)return;
    var dx=e.clientX-gPX, dy=e.clientY-gPY;
    gPX=e.clientX;gPY=e.clientY;
    gYaw+=dx*0.007;
    gPitch+=dy*0.007;
    if(gPitch>1.45)gPitch=1.45;
    if(gPitch<-1.45)gPitch=-1.45;
    gVX=dx*0.007;gVY=dy*0.007;
  });
  function endDrag(e){
    if(!gDrag)return;
    gDrag=false;gSpin=true;
    canvas.classList.remove("dragging");
    gOverlay.classList.remove("dragging");
    try{canvas.releasePointerCapture(e.pointerId);}catch(err){}
  }
  canvas.addEventListener("pointerup",endDrag);
  canvas.addEventListener("pointercancel",endDrag);
  canvas.addEventListener("wheel",function(e){
    e.preventDefault();
    gDist*= (e.deltaY>0)?1.08:0.92;
    if(gDist<1.55)gDist=1.55;
    if(gDist>6)gDist=6;
  },{passive:false});
  canvas.addEventListener("dblclick",function(){
    gYaw=0.7;gPitch=0.35;gDist=2.85;gVX=0;gVY=0;gSpin=true;
  });
}

function globeMats(w,h){
  var asp=w/Math.max(h,1);
  var proj=m4Persp(0.7,asp,0.1,50);
  var view=m4View(gYaw,gPitch,gDist);
  var model=m4Model();
  var mvp=m4Mul(proj,m4Mul(view,model));
  return {proj:proj,view:view,model:model,mvp:mvp,asp:asp};
}

function sunUnit(){
  var sp=solarPosition(new Date());
  return llToXYZ(sp.lon,sp.decl,1);
}

function globeFrame(ts){
  gRAF=requestAnimationFrame(globeFrame);
  if(viewMode!=="3d"||!gReady)return;
  // inertia + idle spin
  if(!gDrag){
    gYaw+=gVX;
    gPitch+=gVY;
    if(gPitch>1.45){gPitch=1.45;gVY=0;}
    if(gPitch<-1.45){gPitch=-1.45;gVY=0;}
    gVX*=0.94;gVY*=0.94;
    if(Math.abs(gVX)<0.00015)gVX=0;
    if(Math.abs(gVY)<0.00015)gVY=0;
    if(gSpin&&gVX===0&&gVY===0)gYaw+=0.0022;
  }
  drawGlobe(gLastState,gLastState?gLastState.gaze:null);
}

function drawGlobe(s,g){
  if(!gReady)return;
  sizeGlobe();
  var canvas=document.getElementById("globe");
  var w=canvas._w,h=canvas._h;
  if(!w||!h)return;
  var dpr=canvas._dpr||1;
  var M=globeMats(w,h);
  var sun=sunUnit();
  // camera world position (inverse orbit): camera sits at +Z after view
  var camEye=[0,0,gDist];
  // transform eye into model/world: view is world→cam, so world cam = inverse(view)*0
  // For lighting we need sun in view/world with model=I: sun is world-space lon/lat — good.
  // uCam in world space (model space): inverse of view rotation applied to eye.
  var invYaw=m4Mul(m4RotY(gYaw),m4Mul(m4RotX(gPitch),m4Ident()));
  var camW=m4Apply(invYaw,[0,0,gDist,1]);

  gl.enable(gl.DEPTH_TEST);
  gl.enable(gl.CULL_FACE);
  gl.cullFace(gl.BACK);
  gl.clearColor(0.04,0.055,0.1,1);
  gl.clear(gl.COLOR_BUFFER_BIT|gl.DEPTH_BUFFER_BIT);

  // stars
  if(glProgStar){
    gl.useProgram(glProgStar);
    var spU=gl.getUniformLocation(glProgStar,"uMVP");
    gl.uniformMatrix4fv(spU,false,new Float32Array(M.mvp));
    var sa=gl.getAttribLocation(glProgStar,"aPos");
    gl.bindBuffer(gl.ARRAY_BUFFER,glStars);
    gl.enableVertexAttribArray(sa);
    gl.vertexAttribPointer(sa,3,gl.FLOAT,false,0,0);
    gl.depthMask(false);
    gl.drawArrays(gl.POINTS,0,800);
    gl.depthMask(true);
    gl.disableVertexAttribArray(sa);
  }

  // sphere
  gl.useProgram(glProg);
  gl.uniformMatrix4fv(gl.getUniformLocation(glProg,"uMVP"),false,new Float32Array(M.mvp));
  gl.uniformMatrix4fv(gl.getUniformLocation(glProg,"uModel"),false,new Float32Array(M.model));
  gl.uniform3f(gl.getUniformLocation(glProg,"uSun"),sun[0],sun[1],sun[2]);
  gl.uniform3f(gl.getUniformLocation(glProg,"uCam"),camW[0],camW[1],camW[2]);
  gl.activeTexture(gl.TEXTURE0);
  gl.bindTexture(gl.TEXTURE_2D,glTex);
  gl.uniform1i(gl.getUniformLocation(glProg,"uTex"),0);

  var aPos=gl.getAttribLocation(glProg,"aPos");
  var aNor=gl.getAttribLocation(glProg,"aNor");
  var aUV=gl.getAttribLocation(glProg,"aUV");
  gl.bindBuffer(gl.ARRAY_BUFFER,glSphere.pos);
  gl.enableVertexAttribArray(aPos);
  gl.vertexAttribPointer(aPos,3,gl.FLOAT,false,0,0);
  gl.bindBuffer(gl.ARRAY_BUFFER,glSphere.nor);
  gl.enableVertexAttribArray(aNor);
  gl.vertexAttribPointer(aNor,3,gl.FLOAT,false,0,0);
  gl.bindBuffer(gl.ARRAY_BUFFER,glSphere.uv);
  gl.enableVertexAttribArray(aUV);
  gl.vertexAttribPointer(aUV,2,gl.FLOAT,false,0,0);
  gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER,glSphere.idx);
  gl.drawElements(gl.TRIANGLES,glSphere.n,gl.UNSIGNED_SHORT,0);
  gl.disableVertexAttribArray(aPos);
  gl.disableVertexAttribArray(aNor);
  gl.disableVertexAttribArray(aUV);

  // great-circle arcs (elevated) + node points
  var nodes=(s&&s.nodes)||[];
  var links=(g&&g.link_names)||[];
  var posByName={};
  var arcVerts=[];
  var ptVerts=[];
  for(var i=0;i<nodes.length;i++){
    var n=nodes[i];
    var ll=nodeLL(n.name,n.continent);
    posByName[n.name]={ll:ll,n:n,p:llToXYZ(ll[0],ll[1],1.01)};
  }
  for(var li=0;li<links.length;li++){
    var a=posByName[links[li]];
    if(!a)continue;
    var b=llToXYZ(0,0,1);
    var A=a.p;
    var dot=A[0]*b[0]+A[1]*b[1]+A[2]*b[2];
    if(dot>1)dot=1;if(dot<-1)dot=-1;
    var om=Math.acos(dot);
    if(om<1e-6)continue;
    var sinOm=Math.sin(om);
    var prev=null;
    for(var t=0;t<=28;t++){
      var f=t/28;
      var s1=Math.sin((1-f)*om)/sinOm, s2=Math.sin(f*om)/sinOm;
      var x=s1*A[0]+s2*b[0], y=s1*A[1]+s2*b[1], z=s1*A[2]+s2*b[2];
      var len=Math.sqrt(x*x+y*y+z*z)||1;
      var lift=1+0.07*Math.sin(Math.PI*f);
      var p=[x/len*lift,y/len*lift,z/len*lift];
      if(prev){arcVerts.push(prev[0],prev[1],prev[2],p[0],p[1],p[2]);}
      prev=p;
    }
  }
  if(glProgLine&&arcVerts.length){
    gl.useProgram(glProgLine);
    gl.uniformMatrix4fv(gl.getUniformLocation(glProgLine,"uMVP"),false,new Float32Array(M.mvp));
    gl.uniform4f(gl.getUniformLocation(glProgLine,"uColor"),0.35,0.7,1.0,0.9);
    gl.bindBuffer(gl.ARRAY_BUFFER,glArc.buf);
    gl.bufferData(gl.ARRAY_BUFFER,new Float32Array(arcVerts),gl.DYNAMIC_DRAW);
    var la=gl.getAttribLocation(glProgLine,"aPos");
    gl.enableVertexAttribArray(la);
    gl.vertexAttribPointer(la,3,gl.FLOAT,false,0,0);
    gl.lineWidth(1.5);
    gl.drawArrays(gl.LINES,0,arcVerts.length/3);
    gl.disableVertexAttribArray(la);
  }

  // node markers as GL points
  for(var k in posByName){
    var item=posByName[k];
    ptVerts.push(item.p[0],item.p[1],item.p[2]);
  }
  if(glProgLine&&ptVerts.length){
    gl.useProgram(glProgLine);
    gl.uniformMatrix4fv(gl.getUniformLocation(glProgLine,"uMVP"),false,new Float32Array(M.mvp));
    gl.uniform4f(gl.getUniformLocation(glProgLine,"uColor"),1.0,0.9,0.4,1.0);
    gl.bindBuffer(gl.ARRAY_BUFFER,glPts.buf);
    gl.bufferData(gl.ARRAY_BUFFER,new Float32Array(ptVerts),gl.DYNAMIC_DRAW);
    var pa=gl.getAttribLocation(glProgLine,"aPos");
    gl.enableVertexAttribArray(pa);
    gl.vertexAttribPointer(pa,3,gl.FLOAT,false,0,0);
    gl.drawArrays(gl.POINTS,0,ptVerts.length/3);
    gl.disableVertexAttribArray(pa);
  }

  // 2D label overlay: nodes on front hemisphere only
  gOctx.clearRect(0,0,w,h);
  gOctx.font="10px ui-monospace,Menlo,Consolas,monospace";
  gOctx.textAlign="center";
  for(var nm in posByName){
    var it=posByName[nm];
    var clip=m4Apply(M.mvp,[it.p[0],it.p[1],it.p[2],1]);
    if(clip[3]<=0.01)continue;
    var ndcX=clip[0]/clip[3], ndcY=clip[1]/clip[3], ndcZ=clip[2]/clip[3];
    if(ndcZ>1||ndcZ<-1)continue;
    // backface: point on far side of sphere relative to camera
    var eye=m4Apply(M.view,[it.p[0],it.p[1],it.p[2],1]);
    if(eye[2]>-1.0){/* behind camera-ish */}
    var toCam=[-eye[0],-eye[1],-eye[2]];
    var nrm=it.p;
    var facing=nrm[0]*toCam[0]+nrm[1]*toCam[1]+nrm[2]*toCam[2];
    if(facing<=0)continue;
    var sx=(ndcX*0.5+0.5)*w;
    var sy=(-ndcY*0.5+0.5)*h;
    var node=it.n;
    var isM=/master|super/.test(node.name.toLowerCase());
    var isG=node.name.toLowerCase().indexOf("gaze")===0;
    var role=isG?"gaze":(isM?"master":"peer");
    var r=role==="master"?6:5;
    var col=contColors[contIndex(node.continent)%contColors.length];
    gOctx.beginPath();
    gOctx.arc(sx,sy,r,0,Math.PI*2);
    gOctx.fillStyle=node.alive?col:"#3d5a80";
    gOctx.fill();
    gOctx.strokeStyle=node.alive?"#fff":"#8eb1d9";
    gOctx.lineWidth=1.5;
    gOctx.stroke();
    if(node.alive){
      gOctx.beginPath();
      gOctx.arc(sx,sy,r+3,0,Math.PI*2);
      gOctx.strokeStyle=col;
      gOctx.globalAlpha=0.45;
      gOctx.stroke();
      gOctx.globalAlpha=1;
    }
    gOctx.fillStyle="#cfe3ff";
    gOctx.fillText(short(node.name),sx,sy+r+12);
  }
  gOctx.fillStyle="#5f7aa8";
  gOctx.textAlign="right";
  gOctx.fillText(((g&&g.now)||"")+" · WebGL globe · drag · wheel zoom",w-12,h-12);
  gOctx.textAlign="left";
  gOctx.fillText("yaw "+(gYaw*180/Math.PI).toFixed(0)+"° · dist "+gDist.toFixed(2),12,h-12);
}

// Fluid refresh: poll for data, but the DOM is patched in place — the
// SVG shell and static map never tear down, so there is zero flash.
setInterval(load,1000);
load();
window.addEventListener("resize",function(){
  lastW=0;lastH=0; // force shell rebuild on next tick with new size
  if(viewMode==="3d"&&gReady){sizeGlobe();if(gLastState)drawGlobe(gLastState,gLastState.gaze);}
  load();
});
</script>
</body>
</html>`

func (o *observer) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Inject the shared coastline geometry so 2D SVG and 3D globe match.
	html := strings.Replace(pageTmpl, "/*COAST_JSON*/[]", worldmap.LandRingsJSON(), 1)
	_, _ = io.WriteString(w, html)
}

const adminPageTmpl = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>HIVEMIND Admin</title>
<style>
  body {font-family: system-ui, sans-serif; max-width: 900px; margin: 2rem auto; line-height: 1.5; color: #333;}
  h1 {font-size: 1.8rem; margin-bottom: 0.5rem;}
  .status {padding: 0.8rem 1.2rem; margin: 1rem 0; border-radius: 6px; font-weight: bold;}
  .alive {background: #d4edda; color: #155724; border: 1px solid #c3e6cb;}
  .dead {background: #f8d7da; color: #721c24; border: 1px solid #f5c6cb;}
  .endpoint {background: #e2e3e5; margin: 0.5rem 0; padding: 0.5rem; border-radius: 4px; font-family: monospace;}
  button {padding: 0.5rem 1rem; margin-right: 0.5rem; font-size: 0.9rem;}
  .note {font-size: 0.85rem; color: #666; margin-top: 0.5rem;}
</style>
</head>
<body>
<h1>HIVEMIND Admin Control</h1>
<div class="status alive" id="status">Starting...</div>
<div id="state"></div>
<h2>Controls</h2>
<div class="endpoint"><button onclick="fetch('/admin/start', {method: 'POST'})">Start World</button> <span class="note">broadcast world_start to mesh</span></div>
<div class="endpoint"><button onclick="fetch('/admin/stop', {method: 'POST'})">Stop World</button> <span class="note">broadcast world_stop to mesh</span></div>
<div class="endpoint"><button onclick="fetch('/admin/restart', {method: 'POST'})">Restart World</button> <span class="note">stop then start</span></div>
<div class="endpoint"><button onclick="fetch('/admin/derive', {method: 'POST'})">Derive Keys</button> <span class="note">trigger key derivation</span></div>
<div class="endpoint"><a href="/admin/keys" style="color: inherit; text-decoration: underline;">View Key State</a></div>
<script>
function load(){fetch('/state').then(r=>r.json()).then(d=>{let s=document.getElementById('state');s.innerHTML='<pre>'+JSON.stringify(d, null, 2)+'</pre>';let st=document.getElementById('status');st.className=d.alive?'alive':'dead';st.textContent=d.alive?'World ALIVE':'World STOPPED';});}setInterval(load,3000);load();
</script>
</body>
</html>`
