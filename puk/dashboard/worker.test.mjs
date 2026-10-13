import {test} from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {webcrypto} from 'node:crypto';
import worker from './worker.mjs';
import html from './page.mjs';
if(!globalThis.crypto)globalThis.crypto=webcrypto;
const env={NODE_TOKEN:'test-only-key-which-is-not-a-deployed-secret',DASHBOARD_PASSWORD:'test-password'};
const request=(path,options={})=>new Request('https://w.example'+path,options);
test('authentication, CSRF, partial outages, bounded rotation and DOM script',async()=>{
 new vm.Script(html.match(/<script>([\s\S]*?)<\/script>/)[1]);
 assert.equal((await worker.fetch(request('/api/status'),env)).status,401);
 assert.equal((await worker.fetch(request('/api/login',{method:'POST',body:'{}'}),env)).status,403);
 const login=await worker.fetch(request('/api/login',{method:'POST',headers:{origin:'https://w.example'},body:JSON.stringify({password:env.DASHBOARD_PASSWORD})}),env);
 assert.equal(login.status,200);assert.match(login.headers.get('set-cookie'),/HttpOnly; Secure; SameSite=Strict/);
 const cookie=login.headers.get('set-cookie').split(';')[0];
 const saved=globalThis.fetch;
 try {
  globalThis.fetch=async(url,options)=>{
   assert.equal(options.headers.get('Authorization'),'Bearer '+env.NODE_TOKEN);
   if(url.includes('-de.'))throw Error('offline');
   return new Response(JSON.stringify({node:{sampled_at:1},job:{status:'running'}}),{headers:{'content-type':'application/json'}});
  };
  const status=await worker.fetch(request('/api/status',{headers:{cookie}}),env);const d=await status.json();
  assert.equal(d.nodes.length,4);assert.match(d.nodes.find(x=>x.region==='DE').error,/离线/);
  assert.equal((await worker.fetch(request('/api/status',{headers:{cookie:cookie+'forged'}}),env)).status,401);
  assert.equal((await worker.fetch(request('/api/nodes/UK/rotate',{method:'POST',headers:{cookie,origin:'https://evil.example'}}),env)).status,403);
  assert.equal((await worker.fetch(request('/api/nodes/UK/rotate',{method:'POST',headers:{cookie,origin:'https://w.example','idempotency-key':'../etc/passwd'}}),env)).status,400);
 }finally{globalThis.fetch=saved}
});
