param(
 [Parameter(Mandatory=$true)][string]$ConfigPath,
 [Parameter(Mandatory=$true)][string]$CorePath,
 [Parameter(Mandatory=$true)][string]$CoreConfigPath,
 [string]$PukPath=(Join-Path $PSScriptRoot '..\dist\puk.exe')
)
$ErrorActionPreference='Stop'
$ConfigPath=(Resolve-Path $ConfigPath).Path
$CorePath=(Resolve-Path $CorePath).Path
$CoreConfigPath=(Resolve-Path $CoreConfigPath).Path
$PukPath=(Resolve-Path $PukPath).Path
$config=Get-Content $ConfigPath -Raw | ConvertFrom-Json
$headers=@{Authorization=('Bearer '+$config.token)}
$url='http://'+$config.listen
$running=$false
try{Invoke-RestMethod ($url+'/api/status') -Headers $headers -TimeoutSec 2 | Out-Null; $running=$true}catch{}
if(-not $running){
 $dir=Split-Path $ConfigPath
 # Quote each path explicitly because Start-Process rejoins ArgumentList.
 $argv=@('run','--config',('"'+$ConfigPath+'"'),'--core',('"'+$CorePath+'"'),'--core-config',('"'+$CoreConfigPath+'"'))
 $process=Start-Process -FilePath $PukPath -ArgumentList $argv -WindowStyle Hidden -RedirectStandardOutput (Join-Path $dir 'puk.stdout.log') -RedirectStandardError (Join-Path $dir 'puk.stderr.log') -PassThru
 $deadline=[DateTime]::UtcNow.AddSeconds(15)
 do{
  Start-Sleep -Milliseconds 200
  if($process.HasExited){throw 'PUK 启动失败，请查看本地日志。'}
  try{Invoke-RestMethod ($url+'/api/status') -Headers $headers -TimeoutSec 1 | Out-Null; $running=$true}catch{}
 }while(-not $running -and [DateTime]::UtcNow -lt $deadline)
 if(-not $running){throw 'PUK 启动超时；未更改现用系统代理。'}
}
Start-Process $url
