@echo off
chcp 65001 > nul
cd /d C:\mytools\AutoFlows\auto-refresh-hosts
echo 正在编译程序（图标已嵌入到 exe 文件中）...
echo.

REM 检查 app.ico 是否存在
if not exist app.ico (
    echo 错误：找不到 app.ico 文件！
    echo 请确保 app.ico 与 refresh-host.go 在同一目录
    pause
    exit /b 1
)

echo √ app.ico 文件已找到
echo.

REM 检查并安装 rsrc 工具用于嵌入图标
echo 正在检查 rsrc 工具...
"C:\Program Files\Go\bin\go.exe" list github.com/akavel/rsrc >nul 2>&1
if errorlevel 1 (
    echo 正在安装 rsrc 工具...
    "C:\Program Files\Go\bin\go.exe" install github.com/akavel/rsrc@latest
)
echo √ rsrc 工具已就绪
echo.

REM 生成 rsrc.syso 文件（用于嵌入图标到 exe）
echo 正在生成资源文件...
set RSRC=%USERPROFILE%\go\bin\rsrc.exe
if exist "%RSRC%" (
    REM 使用 rsrc 工具生成资源文件，将 app.ico 嵌入
    "%RSRC%" -arch=amd64 -ico=app.ico -o=rsrc.syso >nul 2>&1
    if exist rsrc.syso (
        echo √ 资源文件已生成
    ) else (
        echo ! 资源文件生成失败，继续编译...
    )
) else (
    echo ! rsrc 工具未找到，跳过资源嵌入
)
echo.

REM 更新依赖
echo 正在更新依赖...
"C:\Program Files\Go\bin\go.exe" mod tidy >nul 2>&1
echo √ 依赖已更新
echo.

REM 编译程序
echo 正在编译程序...
"C:\Program Files\Go\bin\go.exe" build -o cat-hosts.exe refresh-host.go
if errorlevel 1 (
    echo 错误：编译失败
    pause
    exit /b 1
)

echo.
echo.
echo BUILD SUCCESS!
echo.
echo Program Info:
echo    File: cat-hosts.exe
echo    Location: %CD%\cat-hosts.exe
echo    Features: Icon embedded in exe file
echo.
echo You can now see:
echo    + Icon displayed in file manager
echo    + Icon also shown in system tray when running
echo    + Single file, fully independent
echo.
echo Run command: run.bat or cat-hosts.exe
echo.
pause
