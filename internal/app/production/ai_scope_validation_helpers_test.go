package production

import "github.com/Hello-CTF/NexTerm/internal/terminal"

type scopeTestTerminal struct{}

func (*scopeTestTerminal) StopRecording() (uint64, error) { return 0, nil }
func (*scopeTestTerminal) RecordingStatus() terminal.RecordingStatus {
	return terminal.RecordingStatus{}
}
func (*scopeTestTerminal) Feed([]byte)                     {}
func (*scopeTestTerminal) Dump(int) []byte                 { return nil }
func (*scopeTestTerminal) Resize(int, int) error           { return nil }
func (*scopeTestTerminal) SetResponseHandler(func([]byte)) {}
func (*scopeTestTerminal) Close()                          {}
