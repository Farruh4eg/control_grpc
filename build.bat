@echo off
setlocal

REM Get the directory where the script is located (project root)
set SCRIPT_DIR=%~dp0
cd /D "%SCRIPT_DIR%"

echo Starting build process in %CD%...
echo IMPORTANT: Please ensure this script is run from a Command Prompt launched "As Administrator".
echo.

REM --- Set GOPROXY for reliable module downloads ---
echo Setting GOPROXY=direct
set GOPROXY=direct
echo.

REM --- Diagnostic: Check for GCC ---
echo DIAGNOSTIC: Checking for GCC in PATH...
where gcc

REM --- Step 1: Generate protobuf code ---
echo [1/5] Running buf generate...
buf generate
IF %ERRORLEVEL% NEQ 0 (
    echo ERROR: buf generate failed!
    goto :cleanup
)
echo buf generate completed successfully.
echo.

REM --- Step 2: Build Server ---
echo [2/5] Building server...
IF NOT EXIST "server" (
    echo ERROR: 'server' directory not found.
    goto :cleanup
)
cd server
echo Enabling CGO for server build (CGO_ENABLED=1) and using -tags cgo
set CGO_ENABLED=1
echo Building server with verbose flags (-v -x)...
go build -v -x -tags cgo -ldflags="-extldflags=-static -s -w" -o ..\server.exe .
IF %ERRORLEVEL% NEQ 0 (
    echo ERROR: Server build failed!
    set CGO_ENABLED=
    cd ..
    goto :cleanup
)
set CGO_ENABLED=
cd ..
echo Server build completed successfully. server.exe is in the project root.
echo.

REM --- Step 3: Build Client ---
echo [3/5] Building client...
IF NOT EXIST "client" (
    echo ERROR: 'client' directory not found.
    goto :cleanup
)
cd client
go build -o ..\client.exe -ldflags="-extldflags=-static -s -w" .
IF %ERRORLEVEL% NEQ 0 (
    echo ERROR: Client build failed!
    cd ..
    goto :cleanup
)
cd ..
echo Client build completed successfully. client.exe is in the project root.
echo.

REM --- Step 4: Build Launcher ---
echo [4/5] Building launcher...
IF NOT EXIST "launcher.go" (
    echo ERROR: 'launcher.go' not found in the project root.
    goto :cleanup
)
go build -ldflags="-extldflags=-static -s -w" launcher.go
IF %ERRORLEVEL% NEQ 0 (
    echo ERROR: Launcher build failed!
    goto :cleanup
)
echo Launcher build completed successfully. launcher.exe is in the project root.
echo.

REM --- Step 5: Build Relay Server ---
echo [5/5] Build Relay Server...
IF NOT EXIST "relay_server.go" (
    echo ERROR: 'relay_server.go' not found in the project root.
    goto :cleanup
)
go build -ldflags="-extldflags=-static -s -w" relay_server.go
IF %ERRORLEVEL% NEQ 0 (
    echo ERROR: Relay server build failed!
    goto :cleanup
)
echo Relay server build completed successfully. relay_server.exe is in the project root.
echo.

echo Build process completed successfully!
echo Final executables (server.exe, client.exe, launcher.exe, relay_server.exe) are in %CD%

:cleanup
REM Unset GOPROXY if it was set by this script
set GOPROXY=
endlocal
