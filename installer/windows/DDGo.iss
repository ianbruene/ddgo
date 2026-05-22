#define MyAppName "DDGo"
#define MyAppExeName "DDGo.exe"

#ifndef MyAppVersion
#define MyAppVersion "0.0.0"
#endif

#ifndef SourceDir
#define SourceDir "..\..\dist\windows\DDGo"
#endif

#ifndef OutputDir
#define OutputDir "..\..\dist"
#endif

[Setup]
AppId={{c51769c5-26a0-4140-9944-73edc573d8a7}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher=DDGo
DefaultDirName={autopf}\DDGo
DefaultGroupName=DDGo
DisableProgramGroupPage=yes
OutputDir={#OutputDir}
OutputBaseFilename=DDGo-windows-amd64-setup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
UninstallDisplayIcon={app}\{#MyAppExeName}

[Files]
Source: "{#SourceDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Additional icons:"; Flags: unchecked

[Icons]
Name: "{group}\DDGo"; Filename: "{app}\{#MyAppExeName}"
Name: "{autodesktop}\DDGo"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "Launch DDGo"; Flags: nowait postinstall skipifsilent
