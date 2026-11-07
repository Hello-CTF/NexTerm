package main

import "strings"

// nativeText is the text the desktop shell shows without the web interface: the tray menu and the startup error
// window. It follows the system language, matched the way the interface matches it (src/i18n/locales.ts).
type nativeText struct {
	Lang           string
	TrayOpen       string
	TrayQuit       string
	StartupTitle   string
	StartupLead    string
	StartupError   string
	StartupDataDir string
	StartupNext    string
	StartupRetry   string
	// StartupLogs holds the log file path at %s.
	StartupLogs string
	StartupNote string
}

var nativeTexts = map[string]nativeText{
	"zh-CN": {
		Lang: "zh-CN", TrayOpen: "打开界面", TrayQuit: "退出 NexTerm",
		StartupTitle: "NexTerm 启动失败", StartupLead: "NexTerm 无法完成启动初始化。",
		StartupError: "错误信息", StartupDataDir: "数据目录", StartupNext: "后续操作",
		StartupRetry: "关闭此窗口，重新启动 NexTerm 重试。",
		StartupLogs:  "如果仍然失败，请查看日志文件 %s，并把上面的错误信息和日志一并反馈。",
		StartupNote:  "本窗口不会尝试修复、删除或迁移数据目录中的文件。在确认数据已备份之前，请不要删除或修改数据目录中的内容。",
	},
	"zh-TW": {
		Lang: "zh-TW", TrayOpen: "開啟介面", TrayQuit: "結束 NexTerm",
		StartupTitle: "NexTerm 啟動失敗", StartupLead: "NexTerm 無法完成啟動初始化。",
		StartupError: "錯誤訊息", StartupDataDir: "資料目錄", StartupNext: "後續操作",
		StartupRetry: "關閉此視窗，重新啟動 NexTerm 再試一次。",
		StartupLogs:  "如果仍然失敗，請查看記錄檔 %s，並將上方的錯誤訊息與記錄一併回報。",
		StartupNote:  "此視窗不會嘗試修復、刪除或移轉資料目錄中的檔案。在確認資料已備份之前，請勿刪除或修改資料目錄中的內容。",
	},
	"en": {
		Lang: "en", TrayOpen: "Open NexTerm", TrayQuit: "Quit NexTerm",
		StartupTitle: "NexTerm failed to start", StartupLead: "NexTerm could not finish starting up.",
		StartupError: "Error", StartupDataDir: "Data directory", StartupNext: "What to do next",
		StartupRetry: "Close this window and start NexTerm again.",
		StartupLogs:  "If it still fails, check the log file %s and report the error above together with the log.",
		StartupNote:  "This window does not try to repair, delete or migrate any file in the data directory. Do not delete or change anything in the data directory until you have confirmed it is backed up.",
	},
	"ja": {
		Lang: "ja", TrayOpen: "NexTerm を開く", TrayQuit: "NexTerm を終了",
		StartupTitle: "NexTerm の起動に失敗しました", StartupLead: "NexTerm の起動処理を完了できませんでした。",
		StartupError: "エラー", StartupDataDir: "データディレクトリ", StartupNext: "次の操作",
		StartupRetry: "このウィンドウを閉じて、NexTerm をもう一度起動してください。",
		StartupLogs:  "それでも失敗する場合は、ログファイル %s を確認し、上のエラーとログを添えて報告してください。",
		StartupNote:  "このウィンドウはデータディレクトリ内のファイルを修復、削除、移行しません。データのバックアップを確認するまで、データディレクトリの内容を削除または変更しないでください。",
	},
	"ko": {
		Lang: "ko", TrayOpen: "NexTerm 열기", TrayQuit: "NexTerm 종료",
		StartupTitle: "NexTerm을 시작하지 못했습니다", StartupLead: "NexTerm이 시작 준비를 마치지 못했습니다.",
		StartupError: "오류", StartupDataDir: "데이터 디렉터리", StartupNext: "다음 단계",
		StartupRetry: "이 창을 닫고 NexTerm을 다시 시작하세요.",
		StartupLogs:  "계속 실패하면 로그 파일 %s을(를) 확인하고 위의 오류와 로그를 함께 보내 주세요.",
		StartupNote:  "이 창은 데이터 디렉터리의 파일을 복구, 삭제 또는 이전하지 않습니다. 데이터가 백업되었는지 확인하기 전에는 데이터 디렉터리의 내용을 삭제하거나 변경하지 마세요.",
	},
	"de": {
		Lang: "de", TrayOpen: "NexTerm öffnen", TrayQuit: "NexTerm beenden",
		StartupTitle: "NexTerm konnte nicht gestartet werden", StartupLead: "NexTerm konnte den Start nicht abschließen.",
		StartupError: "Fehler", StartupDataDir: "Datenverzeichnis", StartupNext: "Nächste Schritte",
		StartupRetry: "Schließen Sie dieses Fenster und starten Sie NexTerm erneut.",
		StartupLogs:  "Wenn es weiterhin fehlschlägt, prüfen Sie die Protokolldatei %s und melden Sie den obigen Fehler zusammen mit dem Protokoll.",
		StartupNote:  "Dieses Fenster versucht nicht, Dateien im Datenverzeichnis zu reparieren, zu löschen oder zu migrieren. Löschen oder ändern Sie nichts im Datenverzeichnis, bevor Sie sich vergewissert haben, dass es gesichert ist.",
	},
	"fr": {
		Lang: "fr", TrayOpen: "Ouvrir NexTerm", TrayQuit: "Quitter NexTerm",
		StartupTitle: "Échec du démarrage de NexTerm", StartupLead: "NexTerm n'a pas pu terminer son démarrage.",
		StartupError: "Erreur", StartupDataDir: "Dossier de données", StartupNext: "Que faire ensuite",
		StartupRetry: "Fermez cette fenêtre et relancez NexTerm.",
		StartupLogs:  "Si l'échec persiste, consultez le fichier journal %s et signalez l'erreur ci-dessus avec le journal.",
		StartupNote:  "Cette fenêtre ne tente pas de réparer, supprimer ni migrer les fichiers du dossier de données. Ne supprimez ni ne modifiez rien dans le dossier de données avant d'avoir vérifié qu'il est sauvegardé.",
	},
	"es": {
		Lang: "es", TrayOpen: "Abrir NexTerm", TrayQuit: "Salir de NexTerm",
		StartupTitle: "NexTerm no pudo iniciarse", StartupLead: "NexTerm no pudo completar el inicio.",
		StartupError: "Error", StartupDataDir: "Directorio de datos", StartupNext: "Qué hacer ahora",
		StartupRetry: "Cierre esta ventana y vuelva a iniciar NexTerm.",
		StartupLogs:  "Si sigue fallando, revise el archivo de registro %s e informe del error anterior junto con el registro.",
		StartupNote:  "Esta ventana no intenta reparar, eliminar ni migrar archivos del directorio de datos. No elimine ni modifique nada en el directorio de datos hasta confirmar que tiene una copia de seguridad.",
	},
	"ru": {
		Lang: "ru", TrayOpen: "Открыть NexTerm", TrayQuit: "Выйти из NexTerm",
		StartupTitle: "Не удалось запустить NexTerm", StartupLead: "NexTerm не смог завершить запуск.",
		StartupError: "Ошибка", StartupDataDir: "Каталог данных", StartupNext: "Что делать дальше",
		StartupRetry: "Закройте это окно и запустите NexTerm снова.",
		StartupLogs:  "Если ошибка повторится, проверьте файл журнала %s и сообщите об ошибке выше, приложив журнал.",
		StartupNote:  "Это окно не пытается восстанавливать, удалять или переносить файлы в каталоге данных. Не удаляйте и не изменяйте содержимое каталога данных, пока не убедитесь, что у вас есть резервная копия.",
	},
}

// matchNativeLocale maps a language tag (BCP 47 or POSIX, such as zh-Hant-TW or zh_TW.UTF-8) to a supported language.
func matchNativeLocale(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, ".@"); i >= 0 {
		tag = tag[:i]
	}
	parts := strings.FieldsFunc(tag, func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) == 0 {
		return ""
	}
	if parts[0] == "zh" {
		for _, part := range parts[1:] {
			if part == "hant" || part == "tw" || part == "hk" || part == "mo" {
				return "zh-TW"
			}
		}
		return "zh-CN"
	}
	if _, ok := nativeTexts[parts[0]]; ok {
		return parts[0]
	}
	return ""
}

// nativeTextFor picks the text for the first supported language; with languages but none supported it is English,
// and with none known it is the source language, as in the interface.
func nativeTextFor(tags []string) nativeText {
	for _, tag := range tags {
		if locale := matchNativeLocale(tag); locale != "" {
			return nativeTexts[locale]
		}
	}
	for _, tag := range tags {
		if strings.TrimSpace(tag) != "" && !strings.EqualFold(strings.TrimSpace(tag), "c") && !strings.EqualFold(strings.TrimSpace(tag), "posix") {
			return nativeTexts["en"]
		}
	}
	return nativeTexts["zh-CN"]
}

func systemNativeText() nativeText {
	return nativeTextFor(systemLanguageTags())
}
