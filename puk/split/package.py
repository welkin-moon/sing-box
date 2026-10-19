#!/usr/bin/env python3
"""Build private client bundles from pinned upstream binaries and a local profile.

No credentials are uploaded. A cache entry is always hashed before reuse.
"""
import argparse
import hashlib
import json
import pathlib
import shutil
import subprocess
import tempfile
import urllib.parse
import zipfile


def download(asset, dest):
    if not dest.exists():
        parsed = urllib.parse.urlparse(asset['url'])
        if parsed.scheme != 'https' or parsed.hostname != 'github.com':
            raise ValueError('asset must originate on GitHub over HTTPS')
        # curl handles GitHub redirects consistently on both Windows and Linux.
        subprocess.run(['curl.exe' if __import__('os').name == 'nt' else 'curl',
                        '-fL', '--retry', '2', '--connect-timeout', '15',
                        '--max-time', '240', '-o', str(dest)+'.part', asset['url']], check=True)
        pathlib.Path(str(dest)+'.part').replace(dest)
    actual = hashlib.sha256(dest.read_bytes()).hexdigest()
    if actual != asset['sha256']:
        raise ValueError('asset SHA-256 mismatch: '+dest.name)


def extract(archive, root):
    root = root.resolve()
    with zipfile.ZipFile(archive) as z:
        for entry in z.infolist():
            target = (root/entry.filename).resolve()
            if not target.is_relative_to(root) or (entry.external_attr >> 16) & 0o170000 == 0o120000:
                raise ValueError('unsafe archive member')
        z.extractall(root)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--profile', required=True, type=pathlib.Path)
    ap.add_argument('--generator', required=True, type=pathlib.Path,
                    help='native puk-split executable for this build host')
    ap.add_argument('--windows-launcher', required=True, type=pathlib.Path)
    ap.add_argument('--out', required=True, type=pathlib.Path)
    ap.add_argument('--cache', required=True, type=pathlib.Path)
    args = ap.parse_args()
    args.out.mkdir(mode=0o700)  # Never overwrite an existing private bundle.
    args.cache.mkdir(parents=True, exist_ok=True)
    assets = json.loads(pathlib.Path(__file__).with_name('assets.json').read_text())
    files = {}
    for key, asset in assets.items():
        files[key] = args.cache/('android.apk' if key == 'android_gui' else key+'.zip')
        download(asset, files[key])
    with tempfile.TemporaryDirectory(prefix='puk-package-') as temp:
        root = pathlib.Path(temp)
        runtime = root/'runtime'
        subprocess.run([str(args.generator.resolve()), 'generate', '--profile',
                        str(args.profile.resolve()), '--out-dir', str(runtime)], check=True)
        win = root/'PUK-Split-Windows-x64'
        android = root/'PUK-Split-Android-arm64'
        win.mkdir(); android.mkdir()
        extract(files['windows_gui'], win)
        extract(files['windows_core'], win/'xray')
        shutil.copy2(args.windows_launcher, win/'puk-split.exe')
        shutil.copy2(args.profile, win/'profile.private.json')
        for target in [win, android]:
            shutil.copy2(runtime/'throne-configs.json', target/'throne-configs.private.json')
            shutil.copy2(runtime/'throne-import.txt', target/'throne-import.private.txt')
        shutil.copy2(files['android_gui'], android/'Throne-2.0.2-arm64-v8a.apk')
        (win/'Start-CLI.cmd').write_text('@echo off\r\ncd /d "%~dp0"\r\npuk-split.exe run --profile profile.private.json --xray xray\\xray.exe\r\npause\r\n', newline='')
        (win/'Start-GUI.cmd').write_text('@echo off\r\ncd /d "%~dp0"\r\nstart "" "Throne\\Throne.exe" -appdata "%~dp0portable-data"\r\n', newline='')
        for target in [win, android]:
            shutil.copy2(pathlib.Path(__file__).with_name('VERIFICATION.md'), target/'VERIFICATION.md')
            (target/'README.txt').write_text(
                'PUK 上下行分离实验包：TCP 上传 CF，下载 HY2，出口 TW direct。\n'
                '本包含私人节点凭据，不要公开转发或上传 GitHub。\n\n'
                'Android 10+ arm64：安装所附官方原签名 Throne APK。\n'
                'Windows 10+ x64：解压后运行 Start-GUI.cmd，使用独立便携配置目录。\n'
                '在 Throne 中复制 throne-import.private.txt 的完整链接，从剪贴板导入。\n'
                '也可将 throne-configs.private.json 作为完整 Xray 配置导入。\n'
                '选择导入的分流节点后连接；不要作为普通 sing-box 订阅拆开导入。\n\n'
                'Windows 命令行备选：运行 Start-CLI.cmd，在应用中设置 SOCKS 127.0.0.1:19080\n'
                '或 HTTP 127.0.0.1:19081，Ctrl+C 结束。此模式不会自动修改系统代理。\n'
                '两种模式择一使用。UDP 双向走 HY2，速度收益需要本地实测。\n'
                '已验证 Windows 独立内核传输；GUI 导入和 Android 真机连接尚未验证。\n'
                '详细记录见 VERIFICATION.md。\n', encoding='utf-8-sig')
            shutil.make_archive(str(args.out/target.name), 'zip', root_dir=target)
        (args.out/'SHA256SUMS').write_text(''.join(
            hashlib.sha256(f.read_bytes()).hexdigest()+'  '+f.name+'\n'
            for f in sorted(args.out.glob('*.zip'))))
    print('Created private Windows and Android bundles:', args.out)


if __name__ == '__main__':
    main()
