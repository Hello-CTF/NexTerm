# Translation glossary

Simplified Chinese in the code is the source text. Every catalog uses these renderings for product terms, so the same
term reads the same everywhere. Terms not listed follow the platform's usual wording (macOS, Windows, common terminal
and SSH software).

## Rules

- Keep `{name}` placeholders and Go verbs (`%s`, `%d`, `%v`, `%q`, `%w`, `%[1]s`) exactly; reorder them only where the
  grammar needs it (indexed verbs for Go when the order changes).
- Keep product and protocol names untranslated: NexTerm, SSH, SFTP, RDP, VNC, WinRM, Docker, Kubernetes, MCP, TOTP,
  Wake-on-LAN, OSC 52, tmux, zsh, fish, PowerShell, ONNX.
- Keep commands, flags, paths, keys and code (`terminal_list`, `read_screen`, `Ctrl+Alt+Shift+Esc`, `~/.ssh`) as they are.
- Chinese full-width punctuation becomes the target language's punctuation; quotes 「」 become the language's quotes.
- Interface text is short: buttons and menu items stay as short as the language allows; hints are complete sentences.
- Plural forms: a key with `{count}` may map to an object of CLDR plural forms (`one`, `few`, `many`, `other`, ...)
  for languages with plurals; `other` is required.

## Terms

| 简体中文 | 繁體中文 | English | 日本語 | 한국어 | Deutsch | Français | Español | Русский |
|---|---|---|---|---|---|---|---|---|
| 持久终端 | 持久終端 | persistent terminal | 永続ターミナル | 영구 터미널 | persistentes Terminal | terminal persistant | terminal persistente | постоянный терминал |
| 临时终端 | 臨時終端 | temporary terminal | 一時ターミナル | 임시 터미널 | temporäres Terminal | terminal temporaire | terminal temporal | временный терминал |
| 持久终端组件 | 持久終端元件 | persistent terminal component | 永続ターミナルコンポーネント | 영구 터미널 구성 요소 | Komponente für persistente Terminals | composant de terminal persistant | componente de terminal persistente | компонент постоянного терминала |
| 工作区 | 工作區 | workspace | ワークスペース | 작업 공간 | Arbeitsbereich | espace de travail | espacio de trabajo | рабочая область |
| 资产 | 資產 | asset | アセット | 자산 | Asset | ressource | recurso | ресурс |
| 主机 | 主機 | host | ホスト | 호스트 | Host | hôte | host | хост |
| 会话 | 工作階段 | session | セッション | 세션 | Sitzung | session | sesión | сеанс |
| 后台会话 | 背景工作階段 | background session | バックグラウンドセッション | 백그라운드 세션 | Hintergrundsitzung | session en arrière-plan | sesión en segundo plano | фоновый сеанс |
| 标签页 | 分頁 | tab | タブ | 탭 | Tab | onglet | pestaña | вкладка |
| 分屏 | 分割畫面 | split pane | 分割ペイン | 분할 창 | geteilter Bereich | volet divisé | panel dividido | разделённая панель |
| 跳板机 | 跳板機 | jump host | 踏み台ホスト | 점프 호스트 | Jump-Host | hôte de rebond | host de salto | промежуточный хост |
| 凭据 | 憑證 | credential | 認証情報 | 자격 증명 | Anmeldedaten | identifiants | credencial | учётные данные |
| 凭据库 | 憑證庫 | credential vault | 認証情報ボールト | 자격 증명 보관함 | Anmeldedaten-Tresor | coffre d'identifiants | almacén de credenciales | хранилище учётных данных |
| 凭据保护 | 憑證保護 | credential protection | 認証情報の保護 | 자격 증명 보호 | Schutz der Anmeldedaten | protection des identifiants | protección de credenciales | защита учётных данных |
| 保护密码 | 保護密碼 | protection password | 保護パスワード | 보호 암호 | Schutzpasswort | mot de passe de protection | contraseña de protección | пароль защиты |
| 已知主机 | 已知主機 | known hosts | 既知のホスト | 알려진 호스트 | bekannte Hosts | hôtes connus | hosts conocidos | известные хосты |
| 主机指纹 | 主機指紋 | host fingerprint | ホストフィンガープリント | 호스트 지문 | Host-Fingerabdruck | empreinte de l'hôte | huella del host | отпечаток хоста |
| 设备 | 裝置 | device | デバイス | 장치 | Gerät | appareil | dispositivo | устройство |
| 设备代理 | 裝置代理程式 | device agent | デバイスエージェント | 장치 에이전트 | Geräte-Agent | agent d'appareil | agente de dispositivo | агент устройства |
| 设备管理 | 裝置管理 | Devices | デバイス管理 | 장치 관리 | Geräteverwaltung | Gestion des appareils | Gestión de dispositivos | Управление устройствами |
| 主机详情 | 主機詳情 | host details | ホストの詳細 | 호스트 세부 정보 | Hostdetails | détails de l'hôte | detalles del host | сведения о хосте |
| 端口转发 | 連接埠轉送 | port forwarding | ポートフォワーディング | 포트 포워딩 | Portweiterleitung | redirection de port | reenvío de puertos | проброс портов |
| 磁盘挂载 | 磁碟掛載 | disk mount | ディスクマウント | 디스크 마운트 | Laufwerkseinbindung | montage de disque | montaje de disco | подключение диска |
| 快捷命令 | 快捷指令 | quick command | クイックコマンド | 빠른 명령 | Schnellbefehl | commande rapide | comando rápido | быстрая команда |
| 批量执行 | 批次執行 | batch run | 一括実行 | 일괄 실행 | Stapelausführung | exécution groupée | ejecución por lotes | пакетный запуск |
| 广播 | 廣播 | broadcast | ブロードキャスト | 브로드캐스트 | Broadcast | diffusion | difusión | трансляция ввода |
| 接管 | 接管 | take over | 引き継ぐ | 인계 | übernehmen | reprendre la main | tomar el control | перехватить управление |
| 终端历史 | 終端歷史 | terminal history | ターミナル履歴 | 터미널 기록 | Terminalverlauf | historique du terminal | historial de terminal | история терминала |
| 状态规则 | 狀態規則 | status rules | ステータスルール | 상태 규칙 | Statusregeln | règles d'état | reglas de estado | правила состояния |
| 告警 | 告警 | alert | アラート | 경고 | Warnung | alerte | alerta | оповещение |
| 静音时段 | 靜音時段 | quiet hours | サイレント時間帯 | 무음 시간대 | Ruhezeiten | heures silencieuses | horas de silencio | тихие часы |
| 只读 | 唯讀 | read-only | 読み取り専用 | 읽기 전용 | schreibgeschützt | lecture seule | solo lectura | только чтение |
| 剪贴板 | 剪貼簿 | clipboard | クリップボード | 클립보드 | Zwischenablage | presse-papiers | portapapeles | буфер обмена |
| 远程桌面 | 遠端桌面 | remote desktop | リモートデスクトップ | 원격 데스크톱 | Remotedesktop | bureau à distance | escritorio remoto | удалённый рабочий стол |
| 技能 | 技能 | skill | スキル | 스킬 | Skill | compétence | habilidad | навык |
| 记忆 | 記憶 | memory | メモリ | 메모리 | Gedächtnis | mémoire | memoria | память |
| AI 信息脱敏 | AI 資訊去識別化 | AI data masking | AI 情報マスキング | AI 정보 마스킹 | KI-Datenmaskierung | masquage des données IA | enmascaramiento de datos de IA | маскирование данных для ИИ |
| AI 拦截规则 | AI 攔截規則 | AI block rules | AI ブロックルール | AI 차단 규칙 | KI-Sperrregeln | règles de blocage IA | reglas de bloqueo de IA | правила блокировки ИИ |
| 生产安全 | 生產安全 | production safety | 本番環境の保護 | 프로덕션 보호 | Produktionsschutz | sécurité de la production | seguridad de producción | защита продакшена |
| 资产包 | 資產包 | asset bundle | アセットバンドル | 자산 번들 | Asset-Paket | lot de ressources | paquete de recursos | пакет ресурсов |
| 账号与同步 | 帳號與同步 | Account and sync | アカウントと同期 | 계정 및 동기화 | Konto und Synchronisierung | Compte et synchronisation | Cuenta y sincronización | Учётная запись и синхронизация |
| 超级管理员 | 超級管理員 | super administrator | スーパー管理者 | 최고 관리자 | Superadministrator | super-administrateur | superadministrador | суперадминистратор |
| 二次验证 | 二次驗證 | two-factor authentication | 二要素認証 | 2단계 인증 | Zwei-Faktor-Authentifizierung | authentification à deux facteurs | autenticación de dos factores | двухфакторная аутентификация |
| 审计 | 稽核 | audit | 監査 | 감사 | Audit | audit | auditoría | аудит |
| 工具 | 工具 | Tools | ツール | 도구 | Werkzeuge | Outils | Herramientas | Инструменты |
| 设置 | 設定 | Settings | 設定 | 설정 | Einstellungen | Paramètres | Configuración | Настройки |
