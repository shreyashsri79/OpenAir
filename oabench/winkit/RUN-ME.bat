@echo off
REM OpenAir Windows baseline. Double-click this file.
REM Wraps the PowerShell script so no execution-policy change is needed.
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0run-baseline.ps1"
echo.
pause
