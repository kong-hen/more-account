Unicode True
ManifestDPIAware true
SetCompressor zlib

; More Account Windows 安装程序脚本（NSIS Modern UI 2）
; 防杀软误报设计：
; 1. 使用标准 zlib 压缩替代高熵 lzma /solid，降低启发式 ML 误报率；
; 2. 采用当前用户级安装（RequestExecutionLevel user），免 UAC 提权拦截且不修改 HKLM 敏感注册表；
; 3. 不在安装器内嵌或动态下载执行第三方外部 exe（Windows 10/11 已内置 WebView2 Runtime）；
; 4. 完整填充 PE 版本信息与标准控制面板卸载注册表。

!include "MUI2.nsh"
!include "FileFunc.nsh"

!ifndef APP_NAME
  !define APP_NAME "More Account"
!endif
!ifndef APP_EXE_NAME
  !define APP_EXE_NAME "MoreAccount.exe"
!endif
!ifndef APP_PUBLISHER
  !define APP_PUBLISHER "More Account"
!endif
!ifndef APP_VERSION
  !define APP_VERSION "1.0.1"
!endif
!ifndef APP_EXE_PATH
  !define APP_EXE_PATH "../../bin/MoreAccount.exe"
!endif
!ifndef APP_ICON_PATH
  !define APP_ICON_PATH "../icon.ico"
!endif
!ifndef OUT_FILE
  !define OUT_FILE "../../bin/MoreAccount-windows-amd64-installer.exe"
!endif

!define REG_APP_KEY "Software\More Account"
!define REG_UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\MoreAccount"

Name "${APP_NAME} ${APP_VERSION}"
OutFile "${OUT_FILE}"
InstallDir "$LOCALAPPDATA\Programs\More Account"
InstallDirRegKey HKCU "${REG_APP_KEY}" "InstallDir"
RequestExecutionLevel user
BrandingText "${APP_NAME} ${APP_VERSION}"

; 安装程序 PE 元数据（完整版本信息有助于降低杀软启发式误报）
VIProductVersion "${APP_VERSION}.0"
VIFileVersion "${APP_VERSION}.0"
VIAddVersionKey "ProductName" "${APP_NAME}"
VIAddVersionKey "CompanyName" "${APP_PUBLISHER}"
VIAddVersionKey "FileDescription" "More Account Installer"
VIAddVersionKey "FileVersion" "${APP_VERSION}.0"
VIAddVersionKey "ProductVersion" "${APP_VERSION}"
VIAddVersionKey "InternalName" "MoreAccountSetup"
VIAddVersionKey "OriginalFilename" "MoreAccount-windows-amd64-installer.exe"
VIAddVersionKey "LegalCopyright" "Copyright (c) 2026 More Account"

; 界面与图标配置
!define MUI_ICON "${APP_ICON_PATH}"
!define MUI_UNICON "${APP_ICON_PATH}"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${APP_EXE_NAME}"
!define MUI_FINISHPAGE_RUN_TEXT "立即启动 ${APP_NAME}"

; 安装向导页面
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

; 卸载向导页面
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

; 语言包（默认简体中文，兼容英文系统）
!insertmacro MUI_LANGUAGE "SimpChinese"
!insertmacro MUI_LANGUAGE "English"

Section "主程序（必选）" SecMain
  SectionIn RO
  SetShellVarContext current
  SetOutPath "$INSTDIR"

  ; 写入主程序
  File "/oname=${APP_EXE_NAME}" "${APP_EXE_PATH}"

  ; 生成标准卸载程序
  WriteUninstaller "$INSTDIR\Uninstall.exe"

  ; 记录安装目录以便覆盖升级自动识别
  WriteRegStr HKCU "${REG_APP_KEY}" "InstallDir" "$INSTDIR"

  ; 创建开始菜单与桌面快捷方式
  CreateDirectory "$SMPROGRAMS\${APP_NAME}"
  CreateShortCut "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE_NAME}" "" "$INSTDIR\${APP_EXE_NAME}" 0
  CreateShortCut "$SMPROGRAMS\${APP_NAME}\卸载 ${APP_NAME}.lnk" "$INSTDIR\Uninstall.exe" "" "$INSTDIR\Uninstall.exe" 0
  CreateShortCut "$DESKTOP\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE_NAME}" "" "$INSTDIR\${APP_EXE_NAME}" 0

  ; 写入 Windows「应用和功能」标准卸载信息
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegStr HKCU "${REG_UNINSTALL_KEY}" "DisplayName" "${APP_NAME}"
  WriteRegStr HKCU "${REG_UNINSTALL_KEY}" "DisplayVersion" "${APP_VERSION}"
  WriteRegStr HKCU "${REG_UNINSTALL_KEY}" "Publisher" "${APP_PUBLISHER}"
  WriteRegStr HKCU "${REG_UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\${APP_EXE_NAME},0"
  WriteRegStr HKCU "${REG_UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${REG_UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "${REG_UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${REG_UNINSTALL_KEY}" "NoRepair" 1
  WriteRegDWORD HKCU "${REG_UNINSTALL_KEY}" "EstimatedSize" "$0"
SectionEnd

Section "Uninstall"
  SetShellVarContext current

  ; 移除快捷方式
  Delete "$DESKTOP\${APP_NAME}.lnk"
  Delete "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk"
  Delete "$SMPROGRAMS\${APP_NAME}\卸载 ${APP_NAME}.lnk"
  RMDir "$SMPROGRAMS\${APP_NAME}"

  ; 移除程序文件与安装目录（安全保留用户主目录下的 .more-account 账号数据）
  Delete "$INSTDIR\${APP_EXE_NAME}"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"

  ; 清理当前用户注册表记录
  DeleteRegKey HKCU "${REG_UNINSTALL_KEY}"
  DeleteRegKey HKCU "${REG_APP_KEY}"
SectionEnd
