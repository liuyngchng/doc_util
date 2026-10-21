@echo off
setlocal enabledelayedexpansion

:: ================================================================
:: build.bat - Windows OCR build script
::
:: Build docutil.exe with OCR support on Windows.
:: Requires MSYS2 (https://www.msys2.org/).
::
:: After installing MSYS2, open "MSYS2 UCRT64" terminal, run once:
::   pacman -Syu
:: Then close terminal, reopen and run once more:
::   pacman -Su
:: After that, you can run this script.
::
:: This script can be run directly in cmd or PowerShell,
:: MSYS2 terminal not needed.
::
:: Usage:
::   build.bat                            direct connection (no proxy)
::   build.bat --proxy http://host:port   use a network proxy
::   build.bat -p http://host:port        short form
::
:: The proxy is injected into pacman, PowerShell download, and Go,
:: which are the only places this script touches the network.
:: Note: the proxy value must not contain spaces or & characters.
:: ================================================================

set "SCRIPT_DIR=%~dp0"
set "ROOT_DIR=%SCRIPT_DIR%..\"
cd /d "%ROOT_DIR%"

:: --- Parse command-line arguments ---
set "PROXY="
:parse_args
if "%~1"=="" goto :args_done
if /i "%~1"=="--proxy" (
    set "PROXY=%~2"
    shift
    shift
    goto :parse_args
)
if /i "%~1"=="-p" (
    set "PROXY=%~2"
    shift
    shift
    goto :parse_args
)
echo [WARNING] Unknown argument ignored: %~1
shift
goto :parse_args
:args_done

:: Show proxy status so the user knows how to use the option
if "%PROXY%"=="" (
    echo [INFO] No proxy specified. If your network requires a proxy,
    echo        rerun with:  build.bat --proxy http://host:port
    echo        Example:     build.bat --proxy http://127.0.0.1:7890
    echo.
    set "BASH_PROXY="
    set "PS_PROXY_ARGS="
) else (
    echo [INFO] Using proxy: %PROXY%
    echo.
    :: prefix injected into every bash command that needs the network
    set "BASH_PROXY=export http_proxy='%PROXY%' https_proxy='%PROXY%' HTTP_PROXY='%PROXY%' HTTPS_PROXY='%PROXY%' && "
    :: arguments injected into the PowerShell Invoke-WebRequest call
    set "PS_PROXY_ARGS=-Proxy '%PROXY%'"
)

:: --- Locate MSYS2 ---
set "MSYS2="
if exist "C:\msys64\ucrt64.exe"      set "MSYS2=C:\msys64"
if exist "D:\msys64\ucrt64.exe"      set "MSYS2=D:\msys64"
if exist "%USERPROFILE%\msys64\ucrt64.exe" set "MSYS2=%USERPROFILE%\msys64"

if "%MSYS2%"=="" (
    echo [ERROR] Cannot find MSYS2 installation directory.
    echo Looked in: C:\msys64, D:\msys64, %%USERPROFILE%%\msys64
    echo Download and install MSYS2 from https://www.msys2.org/
    exit /b 1
)

echo [INFO] MSYS2 found at: %MSYS2%

:: MSYS2 bash / pacman paths
set "BASH=%MSYS2%\usr\bin\bash.exe"
set "PACMAN=%MSYS2%\usr\bin\pacman.exe"

if not exist "%BASH%" (
    echo [ERROR] bash.exe not found: %BASH%
    exit /b 1
)

:: --- Ensure MSYS2 pacman keys are available ---
echo.
echo === Step 1: Check/install MSYS2 packages ===
echo.

:: First update pacman database (to avoid keyring issues)
"%BASH%" -lc "%BASH_PROXY%pacman -Sy --noconfirm 2>/dev/null || true"

:: Install UCRT64 build toolchain + OpenCV
:: --needed skips already-installed packages
set "PKGS=mingw-w64-ucrt-x86_64-go mingw-w64-ucrt-x86_64-gcc mingw-w64-ucrt-x86_64-opencv mingw-w64-ucrt-x86_64-pkg-config mingw-w64-ucrt-x86_64-ninja mingw-w64-ucrt-x86_64-cmake mingw-w64-ucrt-x86_64-binutils mingw-w64-ucrt-x86_64-tools-git"

echo   Installing: %PKGS%
"%BASH%" -lc "%BASH_PROXY%pacman -S --needed --noconfirm %PKGS%"
if %ERRORLEVEL% neq 0 (
    echo [ERROR] pacman install failed.
    echo Please manually run in "MSYS2 UCRT64" terminal:
    echo   pacman -Syu
    echo   pacman -S --needed %PKGS%
    exit /b 1
)

echo [OK] Packages ready.

:: OpenCV 5 only provides opencv5.pc, but opencv.go hardcodes opencv4.
:: Create opencv4.pc -> opencv5.pc symlink in pkgconfig dir.
if not exist "%MSYS2%\ucrt64\lib\pkgconfig\opencv4.pc" (
    mklink "%MSYS2%\ucrt64\lib\pkgconfig\opencv4.pc" "%MSYS2%\ucrt64\lib\pkgconfig\opencv5.pc" >nul 2>&1
    if errorlevel 1 (
        :: mklink needs admin, fallback to file copy
        copy /Y "%MSYS2%\ucrt64\lib\pkgconfig\opencv5.pc" "%MSYS2%\ucrt64\lib\pkgconfig\opencv4.pc" >nul
    )
)

:: --- Build/Prepare Windows onnxruntime ---
echo.
echo === Step 2: Prepare onnxruntime for Windows ===

set "ORT_DIR=%ROOT_DIR%build\deps\onnxruntime\win-x64"
:: onnxruntime Windows prebuilt version
set "ORT_VER=1.22.0"

:: Check if required files already exist
if exist "%ORT_DIR%\include\onnxruntime_c_api.h" (
    if exist "%ORT_DIR%\lib\libonnxruntime.a" (
        if exist "%ORT_DIR%\bin\onnxruntime.dll" (
            echo   onnxruntime already prepared, skipping.
            goto :ort_done
        )
    )
)

:: Download onnxruntime Windows prebuilt package
set "ORT_ZIP=%ROOT_DIR%build\deps\onnxruntime-win-x64-%ORT_VER%.zip"
set "ORT_URL=https://github.com/microsoft/onnxruntime/releases/download/v%ORT_VER%/onnxruntime-win-x64-%ORT_VER%.zip"

:: Clear leftovers from any previous failed run (empty include/, fake .a, etc.)
rmdir /S /Q "%ORT_DIR%" 2>nul

if not exist "%ORT_ZIP%" (
    echo   Downloading onnxruntime %ORT_VER% ...
    powershell -Command "& { $ProgressPreference='SilentlyContinue'; Invoke-WebRequest -Uri '%ORT_URL%' -OutFile '%ORT_ZIP%' %PS_PROXY_ARGS%; }"
)

:: Verify the archive exists and is non-empty. A failed Invoke-WebRequest can
:: leave nothing or a 0-byte file, so check the file size instead of %ERRORLEVEL%
:: (which is unreliable inside an if (...) block).
set "ORT_ZIP_OK="
if exist "%ORT_ZIP%" (
    for %%A in ("%ORT_ZIP%") do if %%~zA gtr 0 set "ORT_ZIP_OK=1"
)
if not defined ORT_ZIP_OK (
    echo [ERROR] onnxruntime download failed or produced an empty file.
    echo   URL: %ORT_URL%
    echo   If your network requires a proxy, rerun with:
    echo     build.bat --proxy http://host:port
    echo   Or manually download the zip and place it at:
    echo     %ORT_ZIP%
    exit /b 1
)
echo [OK] Archive ready: %ORT_ZIP%

echo   Extracting ...
mkdir "%ORT_DIR%" 2>nul
:: Use PowerShell to extract (built-in, no extra tools needed)
powershell -Command "& { Expand-Archive -Path '%ORT_ZIP%' -DestinationPath '%ROOT_DIR%build\deps\onnxruntime\win-x64-tmp' -Force; }"
:: onnxruntime zip extracts to onnxruntime-win-x64-%ORT_VER%/ directory
:: Move to target location
set "ORT_EXTRACTED=%ROOT_DIR%build\deps\onnxruntime\win-x64-tmp\onnxruntime-win-x64-%ORT_VER%"
if not exist "%ORT_EXTRACTED%" (
    :: Some versions extract flat
    echo   Checking extracted structure...
    dir "%ROOT_DIR%build\deps\onnxruntime\win-x64-tmp" 2>nul
)

:: Copy required: include/, lib/onnxruntime.lib, lib/onnxruntime.dll
mkdir "%ORT_DIR%\include" 2>nul
mkdir "%ORT_DIR%\lib" 2>nul
mkdir "%ORT_DIR%\bin" 2>nul

xcopy /Y /Q "%ORT_EXTRACTED%\include\*" "%ORT_DIR%\include\" >nul 2>&1
copy /Y "%ORT_EXTRACTED%\lib\onnxruntime.lib" "%ORT_DIR%\lib\" >nul 2>&1
copy /Y "%ORT_EXTRACTED%\lib\onnxruntime.dll" "%ORT_DIR%\bin\" >nul 2>&1

:: Check onnxruntime.dll in bin directory
if not exist "%ORT_DIR%\bin\onnxruntime.dll" (
    copy /Y "%ORT_EXTRACTED%\bin\onnxruntime.dll" "%ORT_DIR%\bin\" >nul 2>&1
)

:: Clean up temp directory
rmdir /S /Q "%ROOT_DIR%build\deps\onnxruntime\win-x64-tmp" 2>nul

:: ── Final sanity check: the three required files must exist ──
set "ORT_MISSING="
if not exist "%ORT_DIR%\include\onnxruntime_c_api.h" set "ORT_MISSING=1"
if not exist "%ORT_DIR%\lib\onnxruntime.lib"         set "ORT_MISSING=1"
if not exist "%ORT_DIR%\bin\onnxruntime.dll"         set "ORT_MISSING=1"
if defined ORT_MISSING (
    echo [ERROR] onnxruntime files are incomplete after extraction.
    echo   Expected files not found in %ORT_DIR%:
    echo     include\onnxruntime_c_api.h
    echo     lib\onnxruntime.lib
    echo     bin\onnxruntime.dll
    echo   Please check the zip archive at:
    echo     %ORT_ZIP%
    exit /b 1
)

:: --- Use gendef + dlltool to generate MinGW-compatible import library ---
:: MSVC .lib cannot be linked by MinGW, need to generate .def from DLL then .a
echo   Generating MinGW import library ...
"%BASH%" -lc "cd $(cygpath '%ROOT_DIR%') && export PATH=/ucrt64/bin:\$PATH && gendef - build/deps/onnxruntime/win-x64/bin/onnxruntime.dll > build/deps/onnxruntime/win-x64/lib/onnxruntime.def 2>/dev/null && dlltool -d build/deps/onnxruntime/win-x64/lib/onnxruntime.def -l build/deps/onnxruntime/win-x64/lib/libonnxruntime.a -D build/deps/onnxruntime/win-x64/bin/onnxruntime.dll"
if %ERRORLEVEL% neq 0 (
    echo [ERROR] gendef/dlltool failed.
    echo Please ensure MSYS2 has binutils and tools installed: pacman -S --needed mingw-w64-ucrt-x86_64-binutils mingw-w64-ucrt-x86_64-tools-git
    exit /b 1
)

echo [OK] onnxruntime MinGW import lib ready.

:ort_done

:: --- Build ---
echo.
echo === Step 3: Build docutil.exe (Windows OCR) ===

mkdir dist 2>nul

"%BASH%" -lc "cd $(cygpath '%ROOT_DIR%') && export PATH=/ucrt64/bin:\$PATH && %BASH_PROXY%export GOROOT=/ucrt64/lib/go && export CGO_ENABLED=1 && export GOOS=windows && export GOARCH=amd64 && export GOPROXY=https://goproxy.cn,direct && export CGO_CXXFLAGS='-std=c++17' && echo '  Building...' && go build -mod=mod -ldflags='-s -w -H windowsgui' -o dist/docutil.exe . && echo '  Build OK.'"

if %ERRORLEVEL% neq 0 (
    echo [ERROR] Build failed.
    exit /b 1
)

echo [OK] docutil.exe built.

:: --- Collect DLLs ---
echo.
echo === Step 4: Collect DLL dependencies ===

set "DIST_DIR=%ROOT_DIR%dist"
set "PKG_DIR=%DIST_DIR%\docutil-windows-amd64"

:: Clean old directory
if exist "%PKG_DIR%" rmdir /S /Q "%PKG_DIR%"
mkdir "%PKG_DIR%"

:: Copy exe
copy /Y "%DIST_DIR%\docutil.exe" "%PKG_DIR%\" >nul

:: Use MSYS2 bash + ldd to find required DLLs
echo   Finding DLL dependencies...
"%BASH%" -l "%SCRIPT_DIR%collect_dlls.sh"
if %ERRORLEVEL% neq 0 (
    echo [WARNING] DLL collection had errors - non-fatal.
)

:: --- Copy models ---
echo   Copying models...
xcopy /E /I /Q /Y "%ROOT_DIR%models" "%PKG_DIR%\models\" >nul
echo   models/ copied.

:: --- Package ---
echo.
echo === Step 5: Package ===

set "ZIP_NAME=%DIST_DIR%\docutil-windows-amd64-ocr.zip"
if exist "%ZIP_NAME%" del /Q "%ZIP_NAME%"

powershell -Command "& { Compress-Archive -Path '%PKG_DIR%\*' -DestinationPath '%ZIP_NAME%' -Force; }"
if %ERRORLEVEL% neq 0 (
    echo [ERROR] Packaging failed.
    exit /b 1
)

:: Get file size
for %%A in ("%ZIP_NAME%") do set "ZIP_SIZE=%%~zA"
set /a ZIP_SIZE_MB=%ZIP_SIZE%/1048576

echo.
echo ================================================================
echo Build complete!
echo   Output: %ZIP_NAME%  (~%ZIP_SIZE_MB% MB)
echo.
echo Contents:
echo   docutil.exe
echo   models/ (OCR models)
echo   *.dll (runtime libraries)
echo.
echo Extract to same directory, then run docutil.exe to use OCR.
echo ================================================================

:: Clean up intermediate directory (zip is the final artifact)
if exist "%PKG_DIR%" rmdir /S /Q "%PKG_DIR%"
echo [INFO] Intermediate files cleaned up.

endlocal