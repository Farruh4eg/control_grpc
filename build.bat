@echo off
setlocal

REM Get the directory where the script is located (project root)
set SCRIPT_DIR=%~dp0
cd /D "%SCRIPT_DIR%"

echo Starting build process in %CD%...
echo.

REM --- Step 1: Generate protobuf code ---
echo [1/5] Running buf generate...
buf generate
IF %ERRORLEVEL% NEQ 0 (
    echo ERROR: buf generate failed!
    goto :eof
)
echo buf generate completed successfully.
echo.

REM --- Step 2: Build Server ---
echo [2/5] Building server...
IF NOT EXIST "server" (
    echo ERROR: 'server' directory not found.
    goto :eof
)
cd server
go build -o ..\server.exe .
IF %ERRORLEVEL% NEQ 0 (
    echo ERROR: Server build failed!
    cd ..
    goto :eof
)
cd ..
echo Server build completed successfully. server.exe is in the project root.
echo.

REM --- Step 3: Build Client ---
echo [3/5] Building client...
IF NOT EXIST "client" (
    echo ERROR: 'client' directory not found.
    goto :eof
)
cd client
go build -o ..\client.exe .
IF %ERRORLEVEL% NEQ 0 (
    echo ERROR: Client build failed!
    cd ..
    goto :eof
)
cd ..
echo Client build completed successfully. client.exe is in the project root.
echo.

REM --- Step 4: Build Launcher ---
echo [4/5] Building launcher...
IF NOT EXIST "launcher.go" (
    echo ERROR: 'launcher.go' not found in the project root.
    goto :eof
)
go build launcher.go
IF %ERRORLEVEL% NEQ 0 (
    echo ERROR: Launcher build failed!
    goto :eof
)
echo Launcher build completed successfully. launcher.exe is in the project root.
echo.

echo Build process completed successfully!
echo Final executables (server.exe, client.exe, launcher.exe) are in %CD%

REM --- Step 5: Build Relay Server ---
echo [5/5] Build Relay Server...
IF NOT EXIST "relay_server.go" (
    echo ERROR: 'relay_server.go' not found in the project root.
    goto :eof
)
go build relay_server.go
IF %ERRORLEVEL% NEQ 0 (
    echo ERROR: Relay server build failed!
    goto :eof
)
echo Relay server build completed successfully. relay_server.exe is in the project root.
echo.

echo Build process completed successfully!
echo Final executables (server.exe, client.exe, launcher.exe, relay_server.exe) are in %CD%

:eof
endlocal
