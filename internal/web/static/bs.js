!function(w,d,n,l){var s=d.currentScript,q=w.bonestats?.q||[],P=w.PerformanceObserver,M=Math.round,Z=Date.now,v={},o,p,y=0,E=0,j,r=d.referrer,D,A
if(n.globalPrivacyControl||n.doNotTrack==1||w.doNotTrack==1||n.webdriver||!s)return w.bonestats=()=>{}
D=s.dataset.domain||l.hostname
A=s.dataset.api||new URL(s.src).origin+"/api/event"
j=d.hidden?0:Z()
function S(e,x){var u=new URLSearchParams(l.search),b={d:D,n:e,p:l.pathname,w:w.innerWidth,r};["source","medium","campaign","term","content"].map(k=>b[k]=u.get("utm_"+k));b=JSON.stringify({...b,...x});n.sendBeacon(A,b)||fetch(A,{method:"POST",body:b,keepalive:!0})}
function Y(){y=Math.max(y,M(Math.min(1,(w.scrollY+w.innerHeight)/d.documentElement.scrollHeight)*100)||0)}
function L(){E+=j&&Z()-j;j=d.hidden?0:Z();S("leave",{p,sd:y,et:E,...o?0:(o=1,{v})});E=0}
function V(){p!=l.pathname&&(p&&L(),p=l.pathname,y=0,Y(),S("pageview"),r="")}
function O(t,c,x){try{new P(L=>L.getEntries().forEach(c)).observe({type:t,buffered:!0,...x})}catch(e){}}
w.bonestats=(e,x,m)=>S(""+e,{pr:x,rv:m})
q.map(a=>w.bonestats(...a))
var H=history,u=H.pushState,C=0,W=0,F=0,T=0,I={},N=performance.getEntriesByType("navigation")[0]
H.pushState=function(){u.apply(H,arguments);V()}
w.addEventListener("popstate",V)
w.addEventListener("scroll",Y,{passive:!0})
d.addEventListener("visibilitychange",()=>d.hidden?L():j=Z())
P?.supportedEntryTypes?.includes("layout-shift")&&(v.cls=0)
d.hidden||(O("largest-contentful-paint",e=>v.lcp=M(e.startTime)),O("paint",e=>e.name[6]=="c"&&(v.fcp=M(e.startTime))))
O("layout-shift",e=>{var t=e.startTime;e.hadRecentInput||(W&&t-T<1e3&&t-F<5e3?W+=e.value:(W=e.value,F=t),T=t,W>C&&(v.cls=C=M(W*1e3)/1e3))})
O("event",e=>{var k=e.interactionId,a;k&&!(I[k]>=e.duration)&&(I[k]=e.duration,a=Object.values(I).sort((g,h)=>h-g),v.inp=M(a[a.length/50|0]))},{durationThreshold:16})
N?.responseStart>0&&(v.ttfb=M(N.responseStart))
V()}(window,document,navigator,location)
