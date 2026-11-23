!function(w,d,n,l){var s=d.currentScript,q=w.bonestats?.q||[],P=w.PerformanceObserver,M=Math.round,Z=Date.now,v={},o,p,y=0,j,g,a,B=15e3,r=d.referrer,D,A,J=["gclid","gbraid","wbraid","msclkid","ttclid","twclid","li_fat_id"]
try{var K=localStorage.bonestats_ignore=="true"}catch(e){}
if(n.globalPrivacyControl||n.doNotTrack==1||w.doNotTrack==1||n.webdriver||K||!s)return w.bonestats=()=>{}
D=s.dataset.domain||l.hostname
A=s.dataset.api||new URL(s.src).origin+"/api/event"
j=d.hidden?0:Z()
function S(e,x){var u=new URLSearchParams(l.search),b={d:D,n:e,p:l.pathname,w:w.innerWidth,t:n.maxTouchPoints,r,c:J.find(k=>u.has(k))};["source","medium","campaign","term","content"].map(k=>b[k]=u.get("utm_"+k));b=JSON.stringify({...b,...x});n.sendBeacon(A,b)||fetch(A,{method:"POST",body:b,keepalive:!0})}
function Y(){y=Math.max(y,w.scrollY+w.innerHeight)}
function L(x){var t=j&&Z()-j;j=d.hidden?0:Z();Y();S("leave",{p,sd:M(Math.min(1,y/d.documentElement.scrollHeight)*100)||0,et:t,...x||o?0:{v}})}
function G(e){e.isTrusted&&(a=1,!g&&j&&Z()-j>=1e3&&(g=1,L(1)))}
function V(){p!=l.pathname&&(p&&(L(),o=1),p=l.pathname,y=0,S("pageview"),r="")}
function O(t,c,x){try{new P(L=>L.getEntries().forEach(c)).observe({type:t,buffered:!0,...x})}catch(e){}}
w.bonestats=(e,x,m)=>S(""+e,{pr:x,rv:m})
q.map(a=>w.bonestats(...a))
var H=history,C=0,W=0,F=0,T=0,I={},N=performance.getEntriesByType("navigation")[0]
;["pushState","replaceState"].map(k=>{var u=H[k];H[k]=function(){u.apply(H,arguments);V()}})
w.addEventListener("popstate",V)
w.addEventListener("scroll",()=>{a=1;Y()},{passive:!0})
;["pointerdown","keydown","touchstart","wheel"].map(t=>w.addEventListener(t,G,{passive:!0,capture:!0}))
d.addEventListener("visibilitychange",()=>d.hidden?j&&L():j=Z())
w.addEventListener("pagehide",()=>{j&&L();j=0})
w.addEventListener("pageshow",e=>e.persisted&&(j=d.hidden?0:Z(),p=0,o=1,V()))
setInterval(()=>a&&j&&Z()-j>=B&&(a=0,B=Math.min(B*2,24e4),L(1)),5e3)
P?.supportedEntryTypes?.includes("layout-shift")&&(v.cls=0)
d.hidden||(O("largest-contentful-paint",e=>v.lcp=M(e.startTime)),O("paint",e=>e.name[6]=="c"&&(v.fcp=M(e.startTime))))
O("layout-shift",e=>{var t=e.startTime;e.hadRecentInput||(W&&t-T<1e3&&t-F<5e3?W+=e.value:(W=e.value,F=t),T=t,W>C&&(v.cls=C=M(W*1e3)/1e3))})
O("event",e=>{var k=e.interactionId,a;k&&!(I[k]>=e.duration)&&(I[k]=e.duration,a=Object.values(I).sort((g,h)=>h-g),v.inp=M(a[a.length/50|0]))},{durationThreshold:16})
N?.responseStart>0&&(v.ttfb=M(N.responseStart))
d.prerendering?d.addEventListener("prerenderingchange",()=>{j=Z();V()}):V()}(window,document,navigator,location)
