package aicontext

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var weekdayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

func Render(bundle Bundle) string {
	var output strings.Builder
	if !bundle.Now.IsZero() {
		output.WriteString("[Current Time]\n")
		name, offset := bundle.Now.Zone()
		sign := "+"
		if offset < 0 {
			sign, offset = "-", -offset
		}
		zone := fmt.Sprintf("UTC%s%02d:%02d", sign, offset/3600, offset%3600/60)
		if name != "" {
			zone = fmt.Sprintf("%s (%s)", name, zone)
		}
		fmt.Fprintf(&output, "- Time: %s\n", bundle.Now.Format(time.RFC3339))
		fmt.Fprintf(&output, "- Timezone: %s\n", zone)
		fmt.Fprintf(&output, "- Weekday: %s\n", weekdayNames[bundle.Now.Weekday()])
	}
	if bundle.Session != nil {
		output.WriteString("[Session Context]\n")
		fmt.Fprintf(&output, "- Connection: %s (%s)\n", bundle.Session.Name, bundle.Session.Kind)
		if bundle.Session.Host != "" {
			fmt.Fprintf(&output, "- Host: %s\n", bundle.Session.Host)
		}
		if bundle.Session.Username != "" {
			fmt.Fprintf(&output, "- User: %s\n", bundle.Session.Username)
		}
		if bundle.Session.CWD != "" {
			fmt.Fprintf(&output, "- Current Directory: %s\n", bundle.Session.CWD)
		}
	}
	if bundle.Tab != nil {
		output.WriteString("[Terminal Tab]\n")
		fmt.Fprintf(&output, "- Handle: %s\n", bundle.Tab.Handle)
		if bundle.Tab.Kind != "" {
			fmt.Fprintf(&output, "- Kind: %s\n", bundle.Tab.Kind)
		}
		if bundle.Tab.Title != "" {
			fmt.Fprintf(&output, "- Title: %s\n", bundle.Tab.Title)
		}
		if bundle.Tab.CWD != "" {
			fmt.Fprintf(&output, "- Current Directory: %s\n", bundle.Tab.CWD)
		}
		if bundle.Tab.Durable {
			output.WriteString("- Durable: true\n")
		}
		if bundle.Tab.Exited {
			output.WriteString("- Exited: true\n")
		}
	}
	if bundle.Screen != "" {
		output.WriteString("[Current Terminal Screen]\n")
		output.WriteString(bundle.Screen)
		output.WriteByte('\n')
	}
	if len(bundle.Tail) != 0 {
		output.WriteString("[Recent Terminal Output]\n")
		output.WriteString(tailRunes(strings.Join(bundle.Tail, "\n"), 8000))
		output.WriteByte('\n')
	}
	if bundle.Recon != "" {
		output.WriteString("[Environment Recon Snapshot]\n")
		output.WriteString(prefixRunes(bundle.Recon, 12000))
		output.WriteByte('\n')
	}
	if len(bundle.Tables) != 0 {
		output.WriteString("[Database Tables]\n")
		for _, table := range bundle.Tables {
			fmt.Fprintf(&output, "%s.%s\n", table.Schema, table.Name)
		}
	}
	if len(bundle.Containers) != 0 {
		output.WriteString("[Docker Containers]\n")
		for _, container := range bundle.Containers {
			fmt.Fprintf(&output, "%s [%s] %s (%s)\n", container.Name, container.State, container.Image, container.Status)
		}
	}
	if bundle.Selection != "" {
		output.WriteString("[User-Selected Text]\n")
		output.WriteString(bundle.Selection)
		output.WriteByte('\n')
	}
	return output.String()
}

func TrimToBudget(bundle *Bundle) {
	if len(Render(*bundle)) <= BudgetBytes {
		return
	}
	bundle.Selection = ""
	if len(Render(*bundle)) <= BudgetBytes {
		return
	}
	bundle.Tables = nil
	bundle.Containers = nil
	if len(Render(*bundle)) <= BudgetBytes {
		return
	}
	bundle.Recon = prefixBytes(bundle.Recon, 8000)
	if len(Render(*bundle)) <= BudgetBytes {
		return
	}
	bundle.Screen = prefixBytes(bundle.Screen, 16000)
	if len(bundle.Tail) > 40 {
		bundle.Tail = append([]string(nil), bundle.Tail[len(bundle.Tail)-40:]...)
	}
	for len(Render(*bundle)) > BudgetBytes && len(bundle.Tail) > 1 {
		bundle.Tail = bundle.Tail[1:]
	}
	if len(Render(*bundle)) > BudgetBytes {
		bundle.Recon = ""
	}
	for len(Render(*bundle)) > BudgetBytes && bundle.Screen != "" {
		excess := len(Render(*bundle)) - BudgetBytes
		bundle.Screen = prefixBytes(bundle.Screen, len(bundle.Screen)-excess-1)
	}
	if len(Render(*bundle)) > BudgetBytes {
		bundle.Tail = nil
	}
	if len(Render(*bundle)) > BudgetBytes {
		bundle.Tab = nil
	}
	if len(Render(*bundle)) > BudgetBytes {
		bundle.Session = nil
	}
}

func prefixRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func tailRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[len(runes)-limit:])
}

func prefixBytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit]
}
