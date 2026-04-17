@echo off
REM 以管理员身份运行 cat-hosts.exe
cd /d C:\mytools\AutoFlows\auto-refresh-hosts

REM 检查是否已经以管理员身份运行
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo 需要管理员权限，正在请求权限...
    powershell -Command "Start-Process '%~f0' -Verb RunAs"
    exit /b 0
)

echo 启动 Hosts 监控程序...
start "" cat-hosts.exe
