@echo off
REM sol 安装脚本的 Windows 双击入口：用对的执行策略调 install.ps1，用户不需要记 -ExecutionPolicy。
REM 零参数——所有选择都在生成的配置文件和问答里。
setlocal

set "PS=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"
if not exist "%PS%" (
  echo 找不到 PowerShell（%PS%）。请在 PowerShell 里手动运行：
  echo   Set-ExecutionPolicy -Scope Process Bypass -Force; ^& "%~dp0install.ps1"
  pause
  exit /b 1
)

"%PS%" -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1"
set rc=%ERRORLEVEL%
echo.
if not "%rc%"=="0" (
  echo 安装脚本以退出码 %rc% 结束。上面最后几行说明原因。
) else (
  echo 完成。按任意键关闭窗口。
)
pause
exit /b %rc%