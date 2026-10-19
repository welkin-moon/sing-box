#!/usr/bin/env python3
"""Embed the same Go compiler and public example in an entirely offline HTML editor."""
import argparse, base64, json, pathlib
p=argparse.ArgumentParser();p.add_argument('--wasm',type=pathlib.Path,required=True);p.add_argument('--wasm-exec',type=pathlib.Path,required=True);p.add_argument('--out',type=pathlib.Path,required=True);a=p.parse_args()
root=pathlib.Path(__file__).parent
html=(root/'editor.html').read_text().replace('/*EXAMPLE*/null',(root/'library.example.json').read_text().strip(),1)
shim=a.wasm_exec.read_text().replace('</script','<\\/script')
payload=base64.b64encode(a.wasm.read_bytes()).decode()
boot='''globalThis.pukOffline=true;globalThis.pukReady=(async()=>{const go=new Go();const bytes=Uint8Array.from(atob(PAYLOAD),c=>c.charCodeAt(0));const result=await WebAssembly.instantiate(bytes,go.importObject);go.run(result.instance);while(typeof globalThis.pukCompile!=='function')await new Promise(r=>setTimeout(r,0));})();'''.replace('PAYLOAD',json.dumps(payload))
html=html.replace('<script>','<script>'+shim+'</script><script>'+boot+'</script><script>',1)
a.out.parent.mkdir(parents=True,exist_ok=True);a.out.write_text(html)
print('Created offline editor:',a.out)
