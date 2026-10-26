param([int]$ApiPort=8081,[int]$ModelPort=8002,[int]$DatabasePort=5436)
$ErrorActionPreference='Stop'
$projectRoot=Split-Path $PSScriptRoot -Parent
$runtimeRoot=Join-Path $projectRoot '.runtime-postgres'
$logRoot=Join-Path $projectRoot '.runtime-logs'
$binRoot=Join-Path $projectRoot '.runtime-bin'
New-Item -ItemType Directory -Force -Path $runtimeRoot,$logRoot,$binRoot | Out-Null
$pgBin=Join-Path $env:ProgramFiles 'PostgreSQL/17/bin'
if(!(Test-Path (Join-Path $pgBin 'pg_ctl.exe'))){throw 'PostgreSQL 17 tools are required. Use compose.demo.yaml on another machine or install PostgreSQL 17.'}
$bundledPython=Join-Path $env:USERPROFILE '.cache/codex-runtimes/codex-primary-runtime/dependencies/python/python.exe'
$python=if(Test-Path $bundledPython){$bundledPython}else{(Get-Command python).Source}
$env:PYTHONPATH=Join-Path $projectRoot '.runtime-python'
$env:PYTHONIOENCODING='utf-8'
& $python -c 'import catboost, pandas, jsonschema, scipy'
if($LASTEXITCODE -ne 0){throw 'Python dependencies are missing. See README: use Python 3.11/3.12 and the handoff requirements.'}
$pgData=Join-Path $runtimeRoot 'data'
if(!(Test-Path (Join-Path $pgData 'PG_VERSION'))){
    $passwordPath=Join-Path $runtimeRoot 'password.txt'
    [IO.File]::WriteAllText($passwordPath,'local_demo_password')
    & (Join-Path $pgBin 'initdb.exe') -D $pgData -U alfahack -A scram-sha-256 "--pwfile=$passwordPath" --encoding=UTF8 --locale=C
    if($LASTEXITCODE -ne 0){throw 'initdb failed'}
}
& (Join-Path $pgBin 'pg_ctl.exe') -D $pgData status 2>$null | Out-Null
if($LASTEXITCODE -ne 0){
    & (Join-Path $pgBin 'pg_ctl.exe') -D $pgData -l (Join-Path $runtimeRoot 'postgres.log') -o "-p $DatabasePort -h 127.0.0.1" -w -t 30 start
    if($LASTEXITCODE -ne 0){throw 'PostgreSQL startup failed'}
}
$env:PGPASSWORD='local_demo_password'
$env:PGCONNECT_TIMEOUT='5'
$present=& (Join-Path $pgBin 'psql.exe') -h 127.0.0.1 -p $DatabasePort -U alfahack -d postgres -At -c "SELECT 1 FROM pg_database WHERE datname='alfahack_demo';"
if($LASTEXITCODE -ne 0){throw 'Cannot connect to the local demo database'}
if($present -ne '1'){
    & (Join-Path $pgBin 'psql.exe') -h 127.0.0.1 -p $DatabasePort -U alfahack -d postgres -v ON_ERROR_STOP=1 -c 'CREATE DATABASE alfahack_demo;'
    if($LASTEXITCODE -ne 0){throw 'CREATE DATABASE failed'}
}
function Test-LocalHealth([string]$url){try{$null=Invoke-RestMethod -Uri $url -TimeoutSec 2;return $true}catch{return $false}}
if(!(Test-LocalHealth "http://127.0.0.1:$ModelPort/health")){
    $env:PORT="$ModelPort"
    Start-Process -FilePath $python -ArgumentList 'server.py' -WorkingDirectory (Join-Path $projectRoot 'alfa_backend_handoff') -WindowStyle Hidden -RedirectStandardOutput (Join-Path $logRoot 'model.out.log') -RedirectStandardError (Join-Path $logRoot 'model.err.log') | Out-Null
}
if(!(Test-LocalHealth "http://127.0.0.1:$ApiPort/health")){
    $apiBinary=Join-Path $binRoot 'api.exe'
    Push-Location (Join-Path $projectRoot 'backend')
    try{go build -o $apiBinary .;if($LASTEXITCODE -ne 0){throw 'Go build failed'}}finally{Pop-Location}
    $env:DATABASE_URL="postgres://alfahack:local_demo_password@127.0.0.1:$DatabasePort/alfahack_demo?sslmode=disable"
    $env:MODEL_URL="http://127.0.0.1:$ModelPort"
    $env:HTTP_ADDR="127.0.0.1:$ApiPort"
    $env:DEMO_SEED='true'
    $env:FRONTEND_DIR=Join-Path $projectRoot 'frontend/dist'
    Start-Process -FilePath $apiBinary -WorkingDirectory (Join-Path $projectRoot 'backend') -WindowStyle Hidden -RedirectStandardOutput (Join-Path $logRoot 'api.out.log') -RedirectStandardError (Join-Path $logRoot 'api.err.log') | Out-Null
}
for($attempt=0;$attempt -lt 20;$attempt++){
    if((Test-LocalHealth "http://127.0.0.1:$ApiPort/health") -and (Test-LocalHealth "http://127.0.0.1:$ModelPort/health")){
        Write-Host "Ready: http://localhost:$ApiPort"
        Write-Host "Separate PostgreSQL: 127.0.0.1:$DatabasePort; model: 127.0.0.1:$ModelPort"
        exit 0
    }
    Start-Sleep -Seconds 1
}
throw "Services did not become ready. Inspect $logRoot"
