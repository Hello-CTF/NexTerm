package production

import (
	"context"
	"strings"
	"unicode"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// System notification permission states.
const (
	SystemNotificationGranted = "granted"
	SystemNotificationDenied  = "denied"
	SystemNotificationPrompt  = "prompt"
)

// SystemNotificationStatus reports whether this process can show operating
// system notifications. Reason explains an unavailable notifier to the user.
type SystemNotificationStatus struct {
	Available bool `json:"available"`
	// Permission is granted, denied or prompt; empty while unavailable.
	Permission string `json:"permission,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// SystemNotification is one notification shown by the operating system. ID
// replaces an earlier notification with the same ID; TabID is handed back
// when the user clicks it.
type SystemNotification struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	TabID string `json:"tabId,omitempty"`
}

type systemNotificationIDRequest struct {
	ID string `json:"id"`
}

// SystemNotifier shows operating system notifications. The desktop injects
// one; the server has none, as its browser clients notify themselves.
type SystemNotifier interface {
	Status(ctx context.Context) SystemNotificationStatus
	// RequestPermission asks the user where the system requires it.
	RequestPermission(ctx context.Context) SystemNotificationStatus
	Notify(ctx context.Context, notification SystemNotification) error
	// Remove takes a shown notification away, for one the user handled
	// elsewhere.
	Remove(ctx context.Context, id string) error
}

const (
	maxSystemNotificationTitle = 256
	maxSystemNotificationBody  = 1024
	// maxSystemNotificationID bounds the notification and tab ids in bytes.
	maxSystemNotificationID = 128
)

var errSystemNotificationsUnsupported = ipc.NewError(ipc.CodeUnsupported, "此端不支持系统通知")

func registerSystemNotificationCommands(dispatcher *ipc.Dispatcher, notifier SystemNotifier) error {
	unavailable := SystemNotificationStatus{Reason: "此端不支持系统通知"}
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "system_notification_status", func(ctx context.Context, _ *ipc.Call, _ struct{}) (SystemNotificationStatus, error) {
				if notifier == nil {
					return unavailable, nil
				}
				return notifier.Status(ctx), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "system_notification_request", func(ctx context.Context, _ *ipc.Call, _ struct{}) (SystemNotificationStatus, error) {
				if notifier == nil {
					return unavailable, nil
				}
				return notifier.RequestPermission(ctx), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "system_notify", func(ctx context.Context, _ *ipc.Call, input SystemNotification) (any, error) {
				if notifier == nil {
					return nil, errSystemNotificationsUnsupported
				}
				input.ID = strings.TrimSpace(input.ID)
				input.Title = clipNotificationText(input.Title, maxSystemNotificationTitle)
				input.Body = clipNotificationText(input.Body, maxSystemNotificationBody)
				if input.ID == "" || input.Title == "" {
					return nil, ipc.NewError(ipc.CodeBadParam, "系统通知需要 id 和标题")
				}
				if !validNotificationID(input.ID) || (input.TabID != "" && !validNotificationID(input.TabID)) {
					return nil, ipc.NewError(ipc.CodeBadParam, "系统通知 id 无效")
				}
				return nil, notifier.Notify(ctx, input)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "system_notification_remove", func(ctx context.Context, _ *ipc.Call, input systemNotificationIDRequest) (any, error) {
				if notifier == nil {
					return nil, errSystemNotificationsUnsupported
				}
				if strings.TrimSpace(input.ID) == "" || !validNotificationID(input.ID) {
					return nil, ipc.NewError(ipc.CodeBadParam, "缺少通知 id")
				}
				return nil, notifier.Remove(ctx, input.ID)
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

// validNotificationID accepts printable ids of at most maxSystemNotificationID bytes.
func validNotificationID(id string) bool {
	return len(id) <= maxSystemNotificationID && cleanNotificationText(id) == id
}

// cleanNotificationText drops controls and format characters (bidi overrides,
// zero-width spaces) but keeps ZWJ/ZWNJ and the tag characters of
// subdivision flags, the rule the terminal signal scanner applies to
// notification text. Line breaks, tabs and the Unicode line and paragraph separators become spaces.
func cleanNotificationText(text string) string {
	text = strings.ToValidUTF8(text, "\uFFFD")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == '\r' || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d' && (r < 0xe0020 || r > 0xe007f)) {
			return -1
		}
		return r
	}, text)
}

// clipNotificationText keeps one clean line of at most limit runes.
func clipNotificationText(text string, limit int) string {
	text = strings.Join(strings.Fields(cleanNotificationText(text)), " ")
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}
