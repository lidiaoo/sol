@echo off
REM sol installer for Windows: double-click entry point.
REM It only picks the right execution policy and runs install.ps1; no arguments to remember.
setlocal

set "PS=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"
if not exist "%PS%" (
  echo Could not find PowerShell at %PS%.
  echo Run it manually instead:
  echo   Set-ExecutionPolicy -Scope Process Bypass -Force; ^& "%~dp0install.ps1"
  pause
  exit /b 1
)

echo Running the sol installer. It prints what it found, then asks before changing anything.
echo.
echo NOTE: if a SECOND PowerShell window pops up later, that is the administrator step.
echo       It may look blank for a moment, then prints its own progress and closes itself.
echo       Nothing here moves until it finishes.
"%PS%" -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1"
set rc=%ERRORLEVEL%
echo.
if not "%rc%"=="0" (
  echo The installer stopped with exit code %rc%. The last lines above say why.
) else (
  echo Done.
)
echo Press any key to close this window.
pause
exit /b %rc%
