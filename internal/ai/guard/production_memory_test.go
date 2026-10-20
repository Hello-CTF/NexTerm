package guard

import "testing"

func TestSessionMemoryNeverAutoAuthorizesProduction(t *testing.T) {
	ruling := Confirm(KindWriteFS, "写入文件需要确认")
	config := Config{Mode: ReadWrite}

	memory := NewMemory()
	memory.Add(KindWriteFS)
	memory.AddPathRule("write_file", "/var/log/**")

	direct := Resource{Action: "write_file", Path: "/var/log/app.log"}
	if decision := DecideResource(config, ruling, memory, direct); decision.Action != ActionAllow {
		t.Fatalf("session memory decision on ordinary target = %+v", decision)
	}

	production := Resource{Action: "write_file", Path: "/var/log/app.log", Environment: "production"}
	if decision := DecideResource(config, ruling, memory, production); decision.Action != ActionAsk {
		t.Fatalf("session memory auto-authorized production: %+v", decision)
	}
	prodAlias := Resource{Action: "write_file", Path: "/var/log/app.log", Environment: "prod"}
	if decision := DecideResource(config, ruling, memory, prodAlias); decision.Action != ActionAsk {
		t.Fatalf("session memory auto-authorized prod alias: %+v", decision)
	}
	custom := Resource{Action: "write_file", Path: "/var/log/app.log", Environment: "live", Production: true}
	if decision := DecideResource(config, ruling, memory, custom); decision.Action != ActionAsk {
		t.Fatalf("session memory auto-authorized configured production: %+v", decision)
	}
	ordinary := Resource{Action: "write_file", Path: "/var/log/app.log", Environment: "staging"}
	if decision := DecideResource(config, ruling, memory, ordinary); decision.Action != ActionAllow {
		t.Fatalf("session memory rejected ordinary environment: %+v", decision)
	}
}

func TestSilentModeIsNotSessionMemory(t *testing.T) {
	ruling := Confirm(KindWriteFS, "写入文件需要确认")
	memory := NewMemory()
	memory.Add(KindWriteFS)
	production := Resource{Action: "write_file", Path: "/var/log/app.log", Environment: "production"}
	decision := DecideResource(Config{Mode: Silent}, ruling, memory, production)
	if decision.Action != ActionAllow {
		t.Fatalf("silent mode decision = %+v", decision)
	}
	if decision := DecideResource(Config{Mode: Unattended}, ruling, memory, production); decision.Action != ActionDeny {
		t.Fatalf("unattended production decision = %+v", decision)
	}
}
