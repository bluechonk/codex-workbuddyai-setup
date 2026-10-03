; WorkBuddyAI Gateway 安装器脚本（Inno Setup 6）
; 版本号由 build.py 通过 /DAppVersion 传入（唯一来源 src/cli.py 的 __version__）。
; 产物：dist/installer/workbuddyai-gateway-<版本号>-setup.exe
; 用户级安装（免管理员），装到 %LOCALAPPDATA%\Programs\WorkBuddyAI Gateway，
; 自动创建开始菜单快捷方式，桌面快捷方式可选。

#ifndef AppVersion
#define AppVersion "0.1.0"
#endif

#define AppName "WorkBuddyAI Gateway"
#define AppExeName "workbuddyai-gateway-" + AppVersion + ".exe"

[Setup]
AppId={{7C3A1B4E-52D8-4F0A-9B1C-3A9E05D74F10}
AppName={#AppName}
AppVersion={#AppVersion}
AppPublisher=bluechonk
DefaultDirName={autopf}\WorkBuddyAI Gateway
PrivilegesRequired=lowest
OutputDir=dist\installer
OutputBaseFilename=workbuddyai-gateway-{#AppVersion}-setup
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
UninstallDisplayIcon={app}\{#AppExeName}
CloseApplications=yes
RestartApplications=no

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; \
    GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "dist\workbuddyai-gateway-{#AppVersion}\*"; DestDir: "{app}"; \
    Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{autoprograms}\{#AppName}"; Filename: "{app}\{#AppExeName}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExeName}"; \
    Tasks: desktopicon

[Run]
Filename: "{app}\{#AppExeName}"; Description: "{cm:LaunchProgram,{#AppName}}"; \
    Flags: nowait postinstall skipifsilent
