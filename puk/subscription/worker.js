addEventListener("fetch", event => {
  event.respondWith(handle(event.request));
});

const PANEL_HOST = "panel-puk.20100823.xyz";
const SUB_HOST = "sub-puk.20100823.xyz";
const SUB_BASE = "https://sub-puk.20100823.xyz/sub/";
const EDGE_SUBSCRIPTIONS = [
  { region: "us", host: "us-sui.20100823.xyz" },
  { region: "de", host: "de-sui.20100823.xyz" },
  { region: "jp", host: "jp-sui.20100823.xyz" }
];
const CLOUDFLARED_HOSTS = new Set([
  "ws-puk.20100823.xyz",
  "us-ws.20100823.xyz",
  "de-ws.20100823.xyz",
  "jp-ws.20100823.xyz"
]);

async function handle(request) {
  const url = new URL(request.url);

  if (url.hostname !== PANEL_HOST && url.hostname !== SUB_HOST) {
    return fetch(request);
  }

  if (url.protocol !== "https:") {
    url.protocol = "https:";
    return Response.redirect(url.toString(), 308);
  }

  if (url.hostname === SUB_HOST) {
    if (request.method === "GET" && url.pathname.startsWith("/sub/")) {
      return unifiedSubscription(request, url);
    }

    const response = await fetch(request);
    return copyNoStore(response);
  }

  if (url.pathname === "/" || url.pathname === "") {
    return Response.redirect("https://panel-puk.20100823.xyz/app/", 302);
  }

  if (url.pathname.startsWith("/sub/")) {
    const target = new URL(url.pathname + url.search, "https://sub-puk.20100823.xyz");
    return Response.redirect(target.toString(), 308);
  }

  if (url.pathname === "/.well-known/puk-edge-health") {
    return new Response(JSON.stringify({
      ok: true,
      panel: "https://panel-puk.20100823.xyz/",
      subscription_base: SUB_BASE,
      subscription_edge: true,
      aggregation: "uk+us+de+jp"
    }), {
      headers: {
        "content-type": "application/json; charset=utf-8",
        "cache-control": "no-store"
      }
    });
  }

  let upstreamRequest = request;

  if (url.pathname === "/app/api/save" && request.method === "POST") {
    const ct = request.headers.get("content-type") || "";
    if (ct.includes("application/x-www-form-urlencoded")) {
      const body = await request.clone().text();
      const params = new URLSearchParams(body);
      if (params.get("object") === "settings") {
        const raw = params.get("data");
        if (raw) {
          try {
            const data = JSON.parse(raw);
            data.subURI = SUB_BASE;
            params.set("data", JSON.stringify(data));
            const headers = new Headers(request.headers);
            headers.set("content-type", "application/x-www-form-urlencoded; charset=UTF-8");
            headers.delete("content-length");
            upstreamRequest = new Request(request, {
              body: params.toString(),
              headers
            });
          } catch (_) {}
        }
      }
    }
  }

  const response = await fetch(upstreamRequest);

  if (!url.pathname.startsWith("/app/api/")) {
    return response;
  }

  const ct = response.headers.get("content-type") || "";
  if (!ct.includes("application/json")) {
    return response;
  }

  try {
    const data = await response.clone().json();
    rewriteSubURI(data);

    const headers = new Headers(response.headers);
    headers.set("content-type", "application/json; charset=utf-8");
    headers.set("cache-control", "no-store");
    headers.delete("content-length");

    return new Response(JSON.stringify(data), {
      status: response.status,
      statusText: response.statusText,
      headers
    });
  } catch (_) {
    return response;
  }
}

function isBase64Format(url) {
  const format = (url.searchParams.get("format") || "").toLowerCase();
  return format === "" || format === "base64" || format === "b64";
}

async function aggregateBase64Subscription(request, url) {
  // Authentication errors from the primary remain authoritative.
  let upstream;
  try { upstream = await fetch(new Request(request, { signal: AbortSignal.timeout(8000) })); }
  catch (_) { upstream = new Response("Primary subscription unavailable", {status:503}); }
  if (!upstream.ok && upstream.status < 500) return copyNoStore(upstream);
  const links = new Map(), available = [], unavailable = [];
  let metadata = upstream.ok ? upstream : null;
  if (upstream.ok) {
    addLinks(links, await upstream.text(), "uk");
    available.push("uk");
    try {
      const jsonURL = new URL(url); jsonURL.searchParams.set("format","json");
      const jsonResponse = await fetch(jsonURL.toString(), {redirect:"follow",signal:AbortSignal.timeout(8000)});
      if (jsonResponse.ok) addJsonCloudflaredLinks(links,await jsonResponse.text(),"uk");
    } catch (_) {}
  } else unavailable.push("uk");
  await Promise.all(EDGE_SUBSCRIPTIONS.map(async edge => {
    try {
      const edgeURL = `http://${edge.host}:2096${url.pathname}`;
      const response = await fetch(edgeURL,{redirect:"follow",signal:AbortSignal.timeout(8000)});
      if (!response.ok) { unavailable.push(edge.region); return; }
      const body = await response.text();
      if (!extractLinks(body).length) { unavailable.push(edge.region); return; }
      if (!metadata) metadata = response;
      addLinks(links,body,edge.region); available.push(edge.region);
      const jsonURL = new URL(edgeURL); jsonURL.searchParams.set("format","json");
      try {
        const jsonResponse = await fetch(jsonURL.toString(),{redirect:"follow",signal:AbortSignal.timeout(8000)});
        if (jsonResponse.ok) addJsonCloudflaredLinks(links,await jsonResponse.text(),edge.region);
      } catch (_) {}
    } catch (_) { unavailable.push(edge.region); }
  }));
  if (!metadata || !links.size) return copyNoStore(upstream.ok ? new Response("No subscription nodes available",{status:503}) : upstream);
  removeRedundantMatrixDual(links);
  const matrixIPv4Hosts = await matrixCloudflaredIPv4(links);
  renameReadablePUKLinks(links);
  for (const raw of Array.from(links.keys())) {
    if (/-old\d+$/.test(decodeURIComponent(new URL(raw).hash.slice(1)))) links.delete(raw);
  }
  const body=encodeBase64(Array.from(links.keys()).join("\n")+"\n");
  const headers=new Headers(metadata.headers);
  headers.set("content-type","text/plain; charset=utf-8");
  headers.set("cache-control","no-store");
  headers.set("x-puk-aggregated",available.sort().join("+"));
  headers.set("x-puk-unavailable-regions",unavailable.sort().join(","));
  headers.set("x-puk-matrix-ipv4-hosts",String(matrixIPv4Hosts||0));
  headers.set("x-puk-node-count",String(links.size));
  headers.delete("content-length");headers.delete("content-encoding");
  return new Response(body,{status:200,headers});
}

function addLinks(target, source, region) {
  for (const link of extractLinks(source)) {
    if (!target.has(link)) target.set(link, region);
  }
}

function addJsonCloudflaredLinks(target, source, region) {
  for (const link of extractJsonCloudflaredLinks(source, region)) {
    if (!isCloudflaredLink(link)) continue;
    if (!target.has(link)) target.set(link, region);
  }
}

function extractLinks(source) {
  const text = (source || "").trim();
  const candidates = [text];
  try {
    candidates.push(atob(text.replace(/\s+/g, "")));
  } catch (_) {}

  const links = [];
  for (const candidate of candidates) {
    for (const line of candidate.split(/\r?\n/)) {
      const value = line.trim();
      if (/^(vless|hysteria2|trojan|ss|vmess|socks5?|http):\/\//i.test(value)) {
        links.push(normalizeLink(value));
      }
    }
  }
  return links;
}

function extractJsonCloudflaredLinks(source, region) {
  let config;
  try {
    config = JSON.parse((source || "").trim());
  } catch (_) {
    return [];
  }

  if (!config || !Array.isArray(config.outbounds)) return [];
  return config.outbounds
    .map(outbound => vlessWsOutboundToLink(outbound, region))
    .filter(Boolean)
    .map(normalizeLink);
}

function vlessWsOutboundToLink(outbound, region) {
  if (!outbound || outbound.type !== "vless" || !outbound.uuid) return null;
  const transport = outbound.transport || {};
  const ukCloudflared = region === "uk" && outbound.tag === "cloudflared";
  const tls = ukCloudflared ? {
    enabled: true,
    server_name: "ws-puk.20100823.xyz"
  } : (outbound.tls || {});
  const server = ukCloudflared ? "ws-puk.20100823.xyz" : outbound.server;
  const serverPort = ukCloudflared ? 443 : outbound.server_port;
  if (transport.type !== "ws" || !tls.enabled || !server || !serverPort) return null;

  const endpointHost = String(server).includes(":") && !String(server).startsWith("[") ? `[${server}]` : server;
  const link = new URL(`vless://${encodeURIComponent(outbound.uuid)}@${endpointHost}:${serverPort}`);
  const query = link.searchParams;
  query.set("encryption", "none");
  query.set("security", tls.reality ? "reality" : "tls");
  query.set("type", "ws");
  query.set("packetEncoding", "xudp");

  if (transport.path) query.set("path", String(transport.path));
  if (transport.early_data_header_name) query.set("eh", String(transport.early_data_header_name));
  const headers = transport.headers || {};
  const hostHeader = headers.Host || headers.host;
  if (hostHeader) query.set("host", Array.isArray(hostHeader) ? String(hostHeader[0]) : String(hostHeader));
  if (tls.server_name) query.set("sni", String(tls.server_name));
  if (tls.utls && tls.utls.fingerprint) query.set("fp", String(tls.utls.fingerprint));
  if (tls.alpn && Array.isArray(tls.alpn) && tls.alpn.length) query.set("alpn", tls.alpn.join(","));
  if (tls.insecure) query.set("allowInsecure", "1");

  const reality = tls.reality || {};
  if (reality.public_key) query.set("pbk", String(reality.public_key));
  const shortId = Array.isArray(reality.short_id) ? reality.short_id[0] : reality.short_id;
  if (shortId) query.set("sid", String(shortId));
  if (outbound.flow) query.set("flow", String(outbound.flow));

  link.hash = outbound.tag ? String(outbound.tag) : "cloudflared-ws";
  return link.toString();
}

function isCloudflaredLink(value) {
  try {
    return CLOUDFLARED_HOSTS.has(new URL(value).hostname.toLowerCase());
  } catch (_) {
    return false;
  }
}

function normalizeLink(value) {
  value = value.replace(/^(vless:\/\/[^@]+)@([0-9a-f:]+):(\d+)([?#])/i, "$1@[$2]:$3$4");
  if (!/^vless:\/\//i.test(value)) return value;
  try {
    const link = new URL(value);
    const packetEncoding = link.searchParams.get("packetEncoding") || link.searchParams.get("packet_encoding");
    if (!packetEncoding || packetEncoding.toLowerCase() !== "xudp") {
      link.searchParams.delete("packet_encoding");
      link.searchParams.set("packetEncoding", "xudp");
    }
    if (decodeURIComponent(link.hash.slice(1)).includes("-Matrix-")) {
      if (link.searchParams.get("type")==="ws") {
        link.searchParams.set("host",link.searchParams.get("host")||link.hostname);
        if (!Number(link.searchParams.get("ed")||0)) link.searchParams.delete("eh");
      }
      link.searchParams.sort();
    }
    return link.toString();
  } catch (_) {
    return value;
  }
}

function encodeBase64(value) {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(binary);
}

function copyNoStore(response) {
  const headers = new Headers(response.headers);
  headers.set("cache-control", "no-store");
  headers.delete("content-length");
  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers
  });
}

function rewriteSubURI(value) {
  if (!value || typeof value !== "object") return;
  if (Array.isArray(value)) {
    for (const item of value) rewriteSubURI(item);
    return;
  }
  for (const key of Object.keys(value)) {
    if (key === "subURI") {
      value[key] = SUB_BASE;
    } else {
      rewriteSubURI(value[key]);
    }
  }
}

function subscriptionFormat(request, url) {
  const f=(url.searchParams.get("format")||"").toLowerCase();
  if(f==="clash"||f==="yaml"||f==="yml") return "clash";
  if(f==="json"||f==="singbox"||f==="sing-box") return "json";
  if(f==="base64"||f==="b64") return "base64";
  if(f) return "upstream";
  return /clash|mihomo/i.test(request.headers.get("user-agent")||"") ? "clash" : "base64";
}

async function unifiedSubscription(request,url) {
  const format=subscriptionFormat(request,url);
  if(format==="upstream") return copyNoStore(await fetch(request));
  const base=new URL(url); base.searchParams.delete("format");
  const upstreamHeaders=new Headers(request.headers); upstreamHeaders.set("user-agent","PUK-Aggregator/1.0");
  const response=await aggregateBase64Subscription(new Request(base.toString(),{method:"GET",headers:upstreamHeaders,redirect:"follow"}),base);
  if(!response.ok||format==="base64") return response;
  const nodes=parseSubscriptionNodes(extractLinks(await response.text()));
  let body,ct;
  if(format==="clash") {
    const proxies=nodes.map(toClashProxy);
    const preferred=nodes.filter(n=>n.ws && CLOUDFLARED_HOSTS.has(n.host)).sort((a,b)=>policyPreference(a.name)-policyPreference(b.name)).map(n=>n.name);
    const order=[...new Set([...preferred,...nodes.map(n=>n.name)])];
    body="mixed-port: 7890\nallow-lan: false\nmode: rule\nlog-level: warning\nproxies:\n"+
      proxies.map(p=>"  - "+JSON.stringify(p)).join("\n")+
      "\nproxy-groups:\n  - "+JSON.stringify({name:"PUK",type:"select",proxies:order})+
      "\nrules:\n  - MATCH,PUK\n";
    ct="text/yaml; charset=utf-8";
  } else {
    const preferred=nodes.filter(n=>n.ws && CLOUDFLARED_HOSTS.has(n.host)).sort((a,b)=>policyPreference(a.name)-policyPreference(b.name)).map(n=>n.name);
    const order=[...new Set([...preferred,...nodes.map(n=>n.name)])];
    body=JSON.stringify({
      log:{level:"warn"},
      dns:{servers:[{type:"local",tag:"dns-local"}],final:"dns-local"},
      inbounds:[{type:"mixed",tag:"mixed-in",listen:"127.0.0.1",listen_port:1080}],
      outbounds:[{type:"selector",tag:"proxy",outbounds:order,default:order[0]},...nodes.map(toSingBoxOutbound),{type:"direct",tag:"direct"}],
      route:{final:"proxy",default_domain_resolver:"dns-local"}
    },null,2); ct="application/json; charset=utf-8";
  }
  const headers=new Headers(response.headers);
  headers.set("content-type",ct);headers.set("cache-control","no-store");
  headers.set("x-puk-format",format);headers.set("x-puk-node-count",String(nodes.length));
  headers.delete("content-length");return new Response(body,{status:200,headers});
}

function parseSubscriptionNodes(links) {
  const names=new Set();
  return links.map((raw,i)=>{
    raw=raw.replace(/^(vless:\/\/[^@]+)@([0-9a-f:]+):(\d+)([?#])/i,"$1@[$2]:$3$4");
    const u=new URL(raw),q=u.searchParams;
    const authority=raw.match(/^[a-z0-9+.-]+:\/\/[^@]+@(\[[^\]]+\]|[^:/?#]+):(\d+)(?:[/?#]|$)/i);
    if(!authority)throw new Error("Invalid node authority");
    const server=authority[1].replace(/^\[|\]$/g,""),port=Number(authority[2]);
    if(!server||port<1||port>65535)throw new Error("Invalid node endpoint");
    let name=decodeURIComponent(u.hash.slice(1))||"PUK-"+(i+1);
    const region={"ws-puk.20100823.xyz":"UK","us-ws.20100823.xyz":"US","de-ws.20100823.xyz":"DE","jp-ws.20100823.xyz":"JP"}[server];
    // Subscription labels have already been normalized consistently.
    const base=name;let count=2;while(names.has(name))name=base+"-"+count++;names.add(name);
    const reality=q.get("security")==="reality";
    const key=(q.get("pbk")||"").replace(/\+/g,"-").replace(/\//g,"_").replace(/=+$/,"");
    if(reality&&atob(key.replace(/-/g,"+").replace(/_/g,"/")+"=".repeat((4-key.length%4)%4)).length!==32)throw new Error("Invalid Reality key");
    return {name,type:u.protocol.slice(0,-1),server,port,auth:decodeURIComponent(u.username),q,
      tls:q.get("security")==="tls"||reality||u.protocol==="hysteria2:",reality,key,
      sni:q.get("sni")||q.get("peer")||server,insecure:["insecure","allowInsecure"].some(k=>q.get(k)==="1"),
      ws:q.get("type")==="ws",path:q.get("path")||"/",host:q.get("host")||server,
      alpn:q.get("alpn")?q.get("alpn").split(","):undefined};
  });
}

function toSingBoxOutbound(n) {
  const o={type:n.type,tag:n.name,server:n.server,server_port:n.port};
  if(n.type==="vless"){o.uuid=n.auth;if(n.q.get("flow"))o.flow=n.q.get("flow");o.packet_encoding="xudp";}
  else if(n.type==="hysteria2")o.password=n.auth;
  else throw new Error("Unsupported node type: "+n.type);
  if(n.tls){
    o.tls={enabled:true,server_name:n.sni};
    if(n.insecure)o.tls.insecure=true;if(n.alpn)o.tls.alpn=n.alpn;
    if(n.reality){o.connect_timeout="15s";o.tls.reality={enabled:true,public_key:n.key,short_id:n.q.get("sid")||""};o.tls.utls={enabled:true,fingerprint:n.q.get("fp")||"chrome"};}
  }
  if(n.ws){
    o.transport={type:"ws",path:n.path,headers:{Host:n.host}};
    const ed=Number(n.q.get("ed")||0);if(ed){o.transport.max_early_data=ed;o.transport.early_data_header_name=n.q.get("eh")||"Sec-WebSocket-Protocol";}
  }
  if(n.ws && n.path.startsWith("/puk-matrix/")) o.domain_resolver={server:"dns-local",strategy:"ipv4_only"};
  return o;
}

function toClashProxy(n) {
  const o={name:n.name,type:n.type,server:n.server,port:n.port,udp:true};
  if(n.type==="vless"){o.uuid=n.auth;if(n.q.get("flow"))o.flow=n.q.get("flow");o["packet-encoding"]="xudp";}
  else if(n.type==="hysteria2")o.password=n.auth;
  else throw new Error("Unsupported node type");
  if(n.tls){o.tls=true;o.servername=n.sni;o.sni=n.sni;if(n.insecure)o["skip-cert-verify"]=true;if(n.alpn)o.alpn=n.alpn;}
  if(n.reality){o["client-fingerprint"]=n.q.get("fp")||"chrome";o["reality-opts"]={"public-key":n.key,"short-id":n.q.get("sid")||""};}
  if(n.ws){o.network="ws";o["ws-opts"]={path:n.path,headers:{Host:n.host}};const ed=Number(n.q.get("ed")||0);if(ed){o["ws-opts"]["max-early-data"]=ed;o["ws-opts"]["early-data-header-name"]=n.q.get("eh")||"Sec-WebSocket-Protocol";}}
  if(n.ws && n.path.startsWith("/puk-matrix/")) o["ip-version"]="ipv4";
  return o;
}

const matrixDNSCache = new Map();
async function matrixCloudflaredIPv4(links) {
  const candidates = Array.from(links.keys()).map(raw => {
    try {
      const u=new URL(raw);
      return decodeURIComponent(u.hash.slice(1)).includes("-Matrix-") &&
        u.searchParams.get("type")==="ws" && CLOUDFLARED_HOSTS.has(u.hostname) ? {raw,u} : null;
    } catch (_) { return null; }
  }).filter(Boolean);
  const ips = new Map();
  await Promise.all(Array.from(new Set(candidates.map(x=>x.u.hostname))).map(async host=>{
    try {
      let cached=matrixDNSCache.get(host);
      if(!cached || cached.expiry<Date.now()) {
        const r=await fetch("https://dns.google/resolve?name="+encodeURIComponent(host)+"&type=A",{headers:{accept:"application/dns-json"},signal:AbortSignal.timeout(3000)});
        if(!r.ok) return;
        const d=await r.json();
        const a=(d.Answer||[]).find(x=>x.type===1 && /^(?:[0-9]{1,3}\.){3}[0-9]{1,3}$/.test(x.data));
        if(!a)return;
        cached={ip:a.data,expiry:Date.now()+Math.min(300,Math.max(30,a.TTL||60))*1000};
        matrixDNSCache.set(host,cached);
      }
      ips.set(host,cached.ip);
    } catch (_) {}
  }));
  for(const {raw,u} of candidates) {
    const ip=ips.get(u.hostname);
    if(!ip)continue;
    const host=u.hostname,region=links.get(raw);
    u.searchParams.set("sni",u.searchParams.get("sni")||host);
    u.searchParams.set("host",u.searchParams.get("host")||host);
    u.hostname=ip;
    u.searchParams.sort();
    links.delete(raw);links.set(u.toString(),region);
  }
  return ips.size;
}

function removeRedundantMatrixDual(links) {
  for (const raw of Array.from(links.keys())) {
    try {
      const name=decodeURIComponent(new URL(raw).hash.slice(1));
      if (/^(US|JP)-Matrix-(Direct|Google-WARP|All-WARP)-(Reality|HY2)-dual$/.test(name)) {
        links.delete(raw);
      }
    } catch (_) {}
  }
}

function renameReadablePUKLinks(links) {
  const items=[];
  // Bare-IP -> region overrides are deploy-time configuration, never committed.
  // Set the PUK_REGION_HOSTS Worker variable (JSON, e.g. {"203.0.113.10":"UK"})
  // in the Cloudflare dashboard to label bare-IP links; without it the
  // heuristics below apply and unknown hosts fall back to "UK".
  let addresses={};
  try {
    const raw=(typeof PUK_REGION_HOSTS!=="undefined"&&PUK_REGION_HOSTS)||"";
    addresses=typeof raw==="string"?(raw?JSON.parse(raw):{}):(raw||{});
  } catch(_) { addresses={}; }
  const modes={"Direct":"direct","Google-WARP":"google","All-WARP":"all"};
  for (const [raw,value] of links) {
    try {
      const u=new URL(raw),old=decodeURIComponent(u.hash.slice(1));
      const matrix=old.match(/^(UK|US|DE|JP)-Matrix-(Direct|Google-WARP|All-WARP)-(Reality|HY2)-(dual|v4|v6)$/);
      const cfMatrix=old.match(/^(UK|US|DE|JP)-Matrix-(Direct|Google-WARP|All-WARP)-Cloudflared$/);
      let base,primary=!!(matrix||cfMatrix);
      if(matrix) {
        base=[matrix[1],matrix[3],matrix[4],modes[matrix[2]]].join("-");
      } else if(cfMatrix) {
        base=[cfMatrix[1],"Cloudflared",modes[cfMatrix[2]]].join("-");
      } else {
        const hostname=u.hostname.replace(/^\[|\]$/g,"");
        const country=(old.match(/^(UK|US|DE|JP)(?:-|$)/i)?.[1]||
          hostname.match(/^(uk|us|de|jp)-(?:sui|ws|node)\./i)?.[1]||
          addresses[hostname]||"UK").toUpperCase();
        const proto=/cloudflared/i.test(old)||u.searchParams.get("type")==="ws" ? "Cloudflared" :
          /^(hysteria2|hy2):$/.test(u.protocol) ? "HY2" : "Reality";
        const policy=/google-direct/i.test(old)?"direct":/all-warp/i.test(old)?"all":"google";
        let family=old.match(/-(dual|v4|v6)(?:$|-)/i)?.[1]?.toLowerCase();
        if(!family)family=hostname.includes(":")?"v6":
          /^(?:\d{1,3}\.){3}\d{1,3}$/.test(hostname)?"v4":
          /^(puk|uk-node|de-node)\.20100823\.xyz$/i.test(hostname)?"dual":
          /^puk6\./i.test(hostname)?"v6":"v4";
        base=proto==="Cloudflared"?[country,proto,policy].join("-"):[country,proto,family,policy].join("-");
      }
      items.push({raw,value,u,old,base,primary});
    } catch (_) {}
  }
  const used=new Set(),oldCounts=new Map();
  for(const item of items.slice().sort((a,b)=>Number(b.primary)-Number(a.primary) ||
    a.base.localeCompare(b.base)||a.old.localeCompare(b.old)||a.raw.localeCompare(b.raw))) {
    let name=item.base;
    if(!item.primary) {
      const index=(oldCounts.get(name)||0)+1;oldCounts.set(name,index);
      name+="-old"+String(index).padStart(2,"0");
    }
    if(used.has(name)) {
      let i=2;const base=name;while(used.has(name))name=base+"-"+i++;
    }
    item.name=name;used.add(name);
  }
  for(const item of items) {
    item.u.hash=item.name;
    links.delete(item.raw);
    links.set(item.u.toString(),item.value);
  }
}

function policyPreference(name) { return /-all$/.test(name)?0:/-google$/.test(name)?1:2; }
