Unicode true
SetCompressor /SOLID lzma
!include "MUI2.nsh"
!include "LogicLib.nsh"

!ifndef NEXTERM_BINARY
  !error "NEXTERM_BINARY is required"
!endif
!ifndef NEXTERM_VERSION
  !error "NEXTERM_VERSION is required"
!endif
!ifndef NEXTERM_NUMERIC_VERSION
  !error "NEXTERM_NUMERIC_VERSION is required"
!endif
!ifndef NEXTERM_OUT
  !error "NEXTERM_OUT is required"
!endif
!ifndef NEXTERM_ICON
  !error "NEXTERM_ICON is required"
!endif
!ifndef NEXTERM_WEBVIEW2
  !error "NEXTERM_WEBVIEW2 is required"
!endif
!ifndef NEXTERM_EXE_NAME
  !error "NEXTERM_EXE_NAME is required"
!endif

Name "NexTerm ${NEXTERM_VERSION}"
OutFile "${NEXTERM_OUT}"
Icon "${NEXTERM_ICON}"
UninstallIcon "${NEXTERM_ICON}"
InstallDir "$LOCALAPPDATA\Programs\NexTerm"
InstallDirRegKey HKCU "Software\NexTerm" "InstallDir"
RequestExecutionLevel user
VIProductVersion "${NEXTERM_NUMERIC_VERSION}"
VIAddVersionKey "ProductName" "NexTerm"
VIAddVersionKey "ProductVersion" "${NEXTERM_VERSION}"
VIAddVersionKey "FileVersion" "${NEXTERM_VERSION}"
VIAddVersionKey "CompanyName" "NexTerm contributors"
VIAddVersionKey "FileDescription" "NexTerm desktop client"

!define MUI_ABORTWARNING
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "NexTerm" SEC01
  SetShellVarContext current
  SetOutPath "$INSTDIR"
  SetOverwrite on
  File "${NEXTERM_BINARY}"
  File /oname=MicrosoftEdgeWebview2Setup.exe "${NEXTERM_WEBVIEW2}"
  WriteUninstaller "$INSTDIR\Uninstall.exe"
  WriteRegStr HKCU "Software\NexTerm" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\NexTerm" "DisplayName" "NexTerm"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\NexTerm" "DisplayVersion" "${NEXTERM_VERSION}"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\NexTerm" "DisplayIcon" "$INSTDIR\${NEXTERM_EXE_NAME}"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\NexTerm" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\NexTerm" "NoModify" 1
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\NexTerm" "NoRepair" 1
  CreateDirectory "$SMPROGRAMS\NexTerm"
  CreateShortcut "$SMPROGRAMS\NexTerm\NexTerm.lnk" "$INSTDIR\${NEXTERM_EXE_NAME}"
  CreateShortcut "$DESKTOP\NexTerm.lnk" "$INSTDIR\${NEXTERM_EXE_NAME}"
  WriteRegStr HKCU "Software\Classes\nexterm" "" "URL:NexTerm protocol"
  WriteRegStr HKCU "Software\Classes\nexterm" "URL Protocol" ""
  WriteRegStr HKCU "Software\Classes\nexterm\DefaultIcon" "" "$INSTDIR\${NEXTERM_EXE_NAME},0"
  WriteRegStr HKCU "Software\Classes\nexterm\shell\open\command" "" '"$INSTDIR\${NEXTERM_EXE_NAME}" "%1"'

  SetRegView 64
  ReadRegStr $0 HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ReadRegStr $1 HKCU "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ${If} $0 == ""
  ${AndIf} $1 == ""
    DetailPrint "Installing the Microsoft Edge WebView2 runtime"
    ExecWait '"$INSTDIR\MicrosoftEdgeWebview2Setup.exe" /silent /install' $2
    ${If} $2 != 0
      Abort "WebView2 runtime installation failed with exit code $2. NexTerm was not installed completely."
    ${EndIf}
  ${EndIf}
  Delete "$INSTDIR\MicrosoftEdgeWebview2Setup.exe"
SectionEnd

Section "Uninstall"
  SetShellVarContext current
  Delete "$DESKTOP\NexTerm.lnk"
  Delete "$SMPROGRAMS\NexTerm\NexTerm.lnk"
  RMDir "$SMPROGRAMS\NexTerm"
  Delete "$INSTDIR\${NEXTERM_EXE_NAME}"
  Delete "$INSTDIR\MicrosoftEdgeWebview2Setup.exe"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"
  DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\NexTerm"
  DeleteRegKey HKCU "Software\NexTerm"
  DeleteRegKey HKCU "Software\Classes\nexterm"
SectionEnd
