import HTML from './page.mjs';
const REGIONS=['UK','DE','US','JP'];
const encoder=new TextEncoder();
const failures=new Map();
function json(v,status=200,headers={}){return new Response(JSON.stringify(v),{status,headers:{'content-type':'application/json','cache-control':'no-store','x-content-type-options':'nosniff',...headers}})}
async function mac(env,text){const k=await crypto.subtle.importKey('raw',encoder.encode(env.NODE_TOKEN),{name:'HMAC',hash:'SHA-256'},false,['sign']);return Array.from(new Uint8Array(await crypto.subtle.sign('HMAC',k,encoder.encode(text)))).map(x=>x.toString(16).padStart(2,'0')).join('')}
function equal(a,b){if(typeof a!=='string'||typeof b!=='string'||a.length!==b.length)return false;let n=0;for(let i=0;i<a.length;i++)n|=a.charCodeAt(i)^b.charCodeAt(i);return n===0}
async function session(req,env){const t=req.headers.get('cookie')?.match(/(?:^|;\s*)__Host-puk=([^;]+)/)?.[1];if(!t)return false;const [expiry,nonce,sig]=t.split('.');if(!/^\d+$/.test(expiry)||!nonce||Number(expiry)<Date.now()/1000||Number(expiry)>Date.now()/1000+43260)return false;return equal(sig,await mac(env,expiry+'.'+nonce))}
async function node(env,region,path,init={}){const h=new Headers(init.headers);h.set('Authorization','Bearer '+env.NODE_TOKEN);const r=await fetch(`https://puk-monitor-${region.toLowerCase()}.20100823.xyz${path}`,{...init,headers:h,redirect:'manual',signal:AbortSignal.timeout(8000)});const d=await r.json();return {status:r.status,data:d}}
export default {async fetch(req,env){
 const url=new URL(req.url);
 if(url.protocol!=='https:')return Response.redirect('https://'+url.host+url.pathname,308);
 if(url.pathname==='/'&&req.method==='GET')return new Response(HTML,{headers:{'content-type':'text/html; charset=utf-8','cache-control':'no-store','x-frame-options':'DENY','x-content-type-options':'nosniff','referrer-policy':'no-referrer','content-security-policy':"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"}});
 if(req.method!=='GET'&&req.headers.get('origin')!==url.origin)return json({error:'origin rejected'},403);
 if(url.pathname==='/api/login'&&req.method==='POST'){
  const ip=req.headers.get('cf-connecting-ip')||'unknown',now=Date.now(),entry=failures.get(ip)||{at:now,n:0};if(now-entry.at>600000){entry.at=now;entry.n=0}if(entry.n>=6)return json({error:'请十分钟后重试'},429);
  if(Number(req.headers.get('content-length')||0)>4096)return json({error:'request too large'},413);
  let body;try{const text=await req.text();if(text.length>4096)return json({error:'request too large'},413);body=JSON.parse(text)}catch{return json({error:'invalid body'},400)}
  if(!equal(body.password,env.DASHBOARD_PASSWORD)){entry.n++;failures.set(ip,entry);if(failures.size>2000)failures.delete(failures.keys().next().value);return json({error:'密码错误'},401)}
  failures.delete(ip);const payload=Math.floor(Date.now()/1000+43200)+'.'+crypto.randomUUID();return json({ok:true},200,{'set-cookie':`__Host-puk=${payload}.${await mac(env,payload)}; Path=/; HttpOnly; Secure; SameSite=Strict; Max-Age=43200`});
 }
 if(!await session(req,env))return json({error:'请先登录'},401);
 if(url.pathname==='/api/logout'&&req.method==='POST')return json({ok:true},200,{'set-cookie':'__Host-puk=; Path=/; HttpOnly; Secure; SameSite=Strict; Max-Age=0'});
 if(url.pathname==='/api/status'&&req.method==='GET'){
  const nodes=await Promise.all(REGIONS.map(async region=>{try{const r=await node(env,region,'/status');return r.status===200?{region,...r.data}:{region,error:'节点接口暂不可用'}}catch(e){return {region,error:'节点离线或请求超时：'+String(e.message).slice(0,160)}}}));return json({at:Math.floor(Date.now()/1000),nodes});
 }
 const match=url.pathname.match(/^\/api\/nodes\/(UK|DE|US|JP)\/rotate$/);
 if(match&&req.method==='POST'){
  const id=req.headers.get('idempotency-key');if(!/^[a-zA-Z0-9_-]{16,64}$/.test(id||''))return json({error:'缺少请求编号'},400);
  try{const r=await node(env,match[1],'/rotate',{method:'POST',headers:{'idempotency-key':id}});return json(r.data,r.status)}catch{return json({error:'提交结果未知，请刷新状态核对；不要重复发起',request_id:id},504)}
 }
 return json({error:'not found'},404);
}};
