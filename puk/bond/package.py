#!/usr/bin/env python3
"""Package generic clients and hub tools. This command never accepts a private profile."""
import argparse,hashlib,pathlib,shutil,tempfile,zipfile
p=argparse.ArgumentParser();p.add_argument('--tools',type=pathlib.Path,required=True);p.add_argument('--out',type=pathlib.Path,required=True);p.add_argument('--apk',type=pathlib.Path);a=p.parse_args();a.out.mkdir(mode=0o700)
root=pathlib.Path(__file__).parent
with tempfile.TemporaryDirectory(prefix='puk-bond-bundle-') as temp:
 base=pathlib.Path(temp)
 for platform in ['Windows-x64','Linux-hub']:
  directory=base/('PUK-Bond-'+platform);directory.mkdir()
  wanted={'Windows-x64':[('puk-bond.exe','puk-bond.exe'),('sing-box.exe','sing-box.exe'),('PUK-Bond-Editor.html','PUK-Bond-Editor.html')],'Linux-hub':[('puk-bond-linux-amd64','puk-bond'),('PUK-Bond-Editor.html','PUK-Bond-Editor.html')]}[platform]
  for source,dest in wanted:
   candidate=a.tools/source
   if not candidate.exists() and source=='puk-bond-linux-amd64':candidate=a.tools/'puk-bond'
   shutil.copy2(candidate,directory/dest)
  for name in ['README.md','VERIFICATION.md','PROTOCOL.md','library.example.json','puk-bond.service']:shutil.copy2(root/name,directory/name)
  shutil.copy2(root.parents[1]/'LICENSE',directory/'LICENSE')
  if platform=='Windows-x64':
   (directory/'Start-GUI.cmd').write_text('@echo off\r\ncd /d "%~dp0"\r\npuk-bond.exe ui --core "%~dp0sing-box.exe"\r\npause\r\n',newline='')
   (directory/'Start-CLI.cmd').write_text('@echo off\r\ncd /d "%~dp0"\r\npuk-bond.exe run --config bond-library.private.json --core "%~dp0sing-box.exe"\r\npause\r\n',newline='')
  (directory/'开始使用.txt').write_text('这是通用实验包，不含实际服务器、订阅或密钥。\nWindows：解压并运行 Start-GUI.cmd，浏览器中导入自己的 sing-box 配置和模块库，选择上传/下载配置档，再连接。\n服务器：安装 Linux-hub 包内的程序和服务文件，使用界面导出的私人服务端配置。\n请先阅读 README.md；私人文件保存在仓库外，原生 UDP 当前未实现。\n',encoding='utf-8-sig')
  shutil.make_archive(str(a.out/directory.name),'zip',root_dir=directory)
 if a.apk:
  directory=base/'PUK-Bond-Android-arm64';directory.mkdir();shutil.copy2(a.apk,directory/'PUK-Bond-Android-arm64-debug.apk');shutil.copy2(a.tools/'PUK-Bond-Editor.html',directory/'PUK-Bond-Editor.html');shutil.copy2(root/'README.md',directory/'README.md');shutil.copy2(root/'VERIFICATION.md',directory/'VERIFICATION.md');shutil.make_archive(str(a.out/directory.name),'zip',root_dir=directory)
 for f in a.out.glob('*.zip'):
  with zipfile.ZipFile(f) as z:
   if z.testzip() is not None:raise ValueError('zip verification failed')
 (a.out/'SHA256SUMS').write_text(''.join(hashlib.sha256(f.read_bytes()).hexdigest()+'  '+f.name+'\n' for f in sorted(a.out.glob('*.zip'))))
print('Created generic packages in',a.out)
