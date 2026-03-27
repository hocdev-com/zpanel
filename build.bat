@echo off
setlocal EnableExtensions EnableDelayedExpansion

set "TARGET=%~1"
set "VERSION=%~2"
if not defined VERSION set "VERSION=0.1.1"

set "ROOT=%~dp0"
if "%ROOT:~-1%"=="\" set "ROOT=%ROOT:~0,-1%"
set "BINARY_NAME=zpanel"
set "CMD_DIR=%ROOT%\cmd\zpanel"
set "BUILD_DIR=%ROOT%\build"
set "CACHE_DIR=%ROOT%\.gocache"
set "ICON_PATH=%ROOT%\assets\app.ico"

if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%" >nul 2>&1
if not exist "%CACHE_DIR%" mkdir "%CACHE_DIR%" >nul 2>&1

call :resolve_target "%TARGET%"
if errorlevel 1 exit /b 1

echo(
echo Selected target: %SELECTED_TARGET%
echo(

if /i "%SELECTED_TARGET%"=="windows" (
    call :build_target windows amd64 "%BUILD_DIR%\windows\%BINARY_NAME%.exe" true
    exit /b %errorlevel%
)

if /i "%SELECTED_TARGET%"=="linux" (
    call :build_target linux amd64 "%BUILD_DIR%\linux\%BINARY_NAME%" false
    exit /b %errorlevel%
)

if /i "%SELECTED_TARGET%"=="all" (
    call :build_target windows amd64 "%BUILD_DIR%\windows\%BINARY_NAME%.exe" true
    if errorlevel 1 exit /b %errorlevel%
    call :build_target linux amd64 "%BUILD_DIR%\linux\%BINARY_NAME%" false
    exit /b %errorlevel%
)

echo Unsupported target "%SELECTED_TARGET%".
exit /b 1

:resolve_target
set "REQUESTED_TARGET=%~1"
if defined REQUESTED_TARGET (
    set "REQUESTED_TARGET=%REQUESTED_TARGET: =%"
)

if /i "%REQUESTED_TARGET%"=="windows" (
    set "SELECTED_TARGET=windows"
    exit /b 0
)
if /i "%REQUESTED_TARGET%"=="linux" (
    set "SELECTED_TARGET=linux"
    exit /b 0
)
if /i "%REQUESTED_TARGET%"=="all" (
    set "SELECTED_TARGET=all"
    exit /b 0
)

if defined REQUESTED_TARGET (
    echo Unsupported target "%~1". Allowed values: windows, linux, all.
    exit /b 1
)

:prompt_target
echo Select build target:
echo   [1] Windows
echo   [2] Linux
echo   [3] All
echo   [Q] Cancel
set /P "CHOICE=Your choice: "
if not defined CHOICE goto prompt_target

if /i "%CHOICE%"=="1" (
    set "SELECTED_TARGET=windows"
    exit /b 0
)
if /i "%CHOICE%"=="2" (
    set "SELECTED_TARGET=linux"
    exit /b 0
)
if /i "%CHOICE%"=="3" (
    set "SELECTED_TARGET=all"
    exit /b 0
)
if /i "%CHOICE%"=="q" (
    echo Build cancelled by user.
    exit /b 1
)

echo Invalid choice. Please select 1, 2, 3, or Q.
echo(
goto prompt_target

:build_target
set "GOOS_VALUE=%~1"
set "GOARCH_VALUE=%~2"
set "OUTPUT_PATH=%~3"
set "EMBED_ICON=%~4"

for %%I in ("%OUTPUT_PATH%") do set "OUTPUT_DIR=%%~dpI"
if not exist "%OUTPUT_DIR%" mkdir "%OUTPUT_DIR%" >nul 2>&1

set "LDFLAGS=-X main.version=%VERSION%"
if /i "%GOOS_VALUE%"=="windows" set "LDFLAGS=-H=windowsgui !LDFLAGS!"

echo Building %GOOS_VALUE%/%GOARCH_VALUE% ^> %OUTPUT_PATH%
set "GOOS=%GOOS_VALUE%"
set "GOARCH=%GOARCH_VALUE%"
set "GOCACHE=%CACHE_DIR%"
go build -trimpath -ldflags "!LDFLAGS!" -o "%OUTPUT_PATH%" "%CMD_DIR%"
if errorlevel 1 (
    echo go build failed for %GOOS_VALUE%/%GOARCH_VALUE%
    exit /b 1
)

if /i "%EMBED_ICON%"=="true" (
    if /i "%OS%"=="Windows_NT" (
        if exist "%ICON_PATH%" (
            go run "%ROOT%\cmd\buildhelper" -exe "%OUTPUT_PATH%" -icon "%ICON_PATH%"
            if errorlevel 1 (
                echo go buildhelper failed for %OUTPUT_PATH%
                exit /b 1
            )
        )
    )
)

echo(
echo Build complete. Output is in %BUILD_DIR%.
exit /b 0
