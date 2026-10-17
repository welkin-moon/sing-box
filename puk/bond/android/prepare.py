#!/usr/bin/env python3
"""Apply the reviewed PUK host overlay to the pinned upstream Android submodule."""
import argparse,pathlib,shutil,xml.etree.ElementTree as ET
p=argparse.ArgumentParser();p.add_argument('--client',type=pathlib.Path,required=True);p.add_argument('--editor',type=pathlib.Path,required=True);a=p.parse_args();root=pathlib.Path(__file__).parent
build=a.client/'app/build.gradle.kts';s=build.read_text();assert 'applicationId = "io.nekohasekai.sfa"' in s
s=s.replace('applicationId = "io.nekohasekai.sfa"','applicationId = "io.nekohasekai.sfa.puk"').replace('minSdk = 24','minSdk = 29').replace('isUniversalApk = true','isUniversalApk = false').replace('include("armeabi-v7a", "arm64-v8a", "x86", "x86_64")','include("arm64-v8a")');build.write_text(s)
ns='http://schemas.android.com/apk/res/android';android='{'+ns+'}';ET.register_namespace('android',ns);ET.register_namespace('tools','http://schemas.android.com/tools')
manifest=a.client/'app/src/main/AndroidManifest.xml';tree=ET.parse(manifest);app=tree.getroot().find('application');app.set(android+'label','PUK Bond')
for activity in app.findall('activity'):
 for intent in activity.findall('intent-filter'):
  if any(c.get(android+'name')=='android.intent.category.LAUNCHER' for c in intent.findall('category')):activity.remove(intent)
activity=ET.SubElement(app,'activity',{android+'name':'.compose.BondBuilderActivity',android+'exported':'true',android+'label':'PUK Bond',android+'configChanges':'orientation|screenSize|smallestScreenSize|keyboardHidden'});intent=ET.SubElement(activity,'intent-filter');ET.SubElement(intent,'action',{android+'name':'android.intent.action.MAIN'});ET.SubElement(intent,'category',{android+'name':'android.intent.category.LAUNCHER'});tree.write(manifest,encoding='utf-8',xml_declaration=True)
target=a.client/'app/src/main/java/io/nekohasekai/sfa/compose/BondBuilderActivity.kt';shutil.copy2(root/'BondBuilderActivity.kt',target)
assets=a.client/'app/src/main/assets';assets.mkdir(exist_ok=True);html=a.editor.read_text();extra='''<script>if(window.PUKHost){window.download=(name,data)=>PUKHost.save(name,JSON.stringify(compiled && data===compiled.config ? compiled.android_config : data,null,2));const b=document.createElement('button');b.textContent='应用到本机代理';b.onclick=()=>{if(compiled)PUKHost.install(JSON.stringify(compiled.android_config))};document.querySelector('main').prepend(b);}</script>''';html=html.replace('</html>',extra+'</html>');(assets/'puk-bond.html').write_text(html)
print('Prepared independent PUK Android host; no user configuration included.')

# The pinned frontend predates two optional libbox platform callbacks. Android's
# ordinary VpnService path does not use platform auto-redirect.
wrapper=a.client/'app/src/main/java/io/nekohasekai/sfa/bg/PlatformInterfaceWrapper.kt'
s=wrapper.read_text();marker='interface PlatformInterfaceWrapper : PlatformInterface {';assert marker in s
if 'override fun usePlatformAutoRedirect()' not in s:
 s=s.replace(marker,marker+'\n    override fun usePlatformAutoRedirect(): Boolean = false\n\n    override fun createAutoRedirect(\n        options: ByteArray,\n        handler: io.nekohasekai.libbox.AutoRedirectHandler,\n    ): io.nekohasekai.libbox.AutoRedirectSession =\n        throw UnsupportedOperationException("Platform auto-redirect is not used by this Android VPN host")\n',1)
wrapper.write_text(s)
