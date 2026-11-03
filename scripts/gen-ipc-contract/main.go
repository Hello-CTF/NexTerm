package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

const legacyCommandLimit = 265

type generation struct {
	commands []commandContract
	events   []eventTopic
	files    map[string][]byte
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gen-ipc-contract: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	root := flag.String("root", ".", "repository root")
	out := flag.String("out", "src/ipc/generated", "generated TypeScript directory")
	check := flag.Bool("check", false, "check generated files without writing")
	flag.Parse()

	absoluteRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	outputDir := *out
	if !filepath.IsAbs(outputDir) {
		outputDir = filepath.Join(absoluteRoot, outputDir)
	}
	generated, err := generateContract(absoluteRoot, nil)
	if err != nil {
		return err
	}
	if *check {
		if err := checkLegacyCommandRatchet(generated.commands); err != nil {
			return err
		}
		return checkGenerated(outputDir, generated.files)
	}
	return writeGenerated(outputDir, generated.files)
}

func checkLegacyCommandRatchet(commands []commandContract) error {
	legacy := 0
	for _, command := range commands {
		if command.Input == nil {
			legacy++
		}
	}
	if legacy > legacyCommandLimit {
		return fmt.Errorf("legacy IPC command count %d exceeds ratchet %d; move commands into a typed scope instead of growing the legacy surface", legacy, legacyCommandLimit)
	}
	return nil
}

func generateContract(root string, overlay map[string][]byte) (*generation, error) {
	packages, err := loadContractPackages(root, overlay)
	if err != nil {
		return nil, err
	}
	commands, err := extractContracts(packages)
	if err != nil {
		return nil, err
	}
	if err := checkReservedInputFields(commands); err != nil {
		return nil, err
	}
	events, err := extractEventTopics(packages)
	if err != nil {
		return nil, err
	}
	files, err := renderContracts(commands, events)
	if err != nil {
		return nil, err
	}
	return &generation{commands: commands, events: events, files: files}, nil
}

var generatedOSMetadata = map[string]bool{
	".DS_Store":   true,
	"Thumbs.db":   true,
	"desktop.ini": true,
}

func checkGenerated(dir string, files map[string][]byte) error {
	var stale []string
	for _, name := range sortedFileNames(files) {
		actual, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				stale = append(stale, name+" (missing)")
				continue
			}
			return err
		}
		if !bytes.Equal(actual, files[name]) {
			stale = append(stale, name)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if generatedOSMetadata[entry.Name()] && !entry.IsDir() {
			continue
		}
		if _, expected := files[entry.Name()]; !expected {
			stale = append(stale, entry.Name()+" (stray)")
		}
	}
	if len(stale) > 0 {
		return fmt.Errorf("generated IPC contract is stale: %s; run task ipc:contract", strings.Join(stale, ", "))
	}
	return nil
}

func writeGenerated(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range sortedFileNames(files) {
		path := filepath.Join(dir, name)
		actual, err := os.ReadFile(path)
		if err == nil && bytes.Equal(actual, files[name]) {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		temporary, err := os.CreateTemp(dir, "."+name+"-*")
		if err != nil {
			return err
		}
		temporaryName := temporary.Name()
		if err = temporary.Chmod(0o644); err == nil {
			_, err = temporary.Write(files[name])
		}
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(temporaryName, path)
		}
		if err != nil {
			_ = os.Remove(temporaryName)
			return err
		}
	}
	return nil
}

func sortedFileNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// reservedTransportFields are lifted out of the request args by both transports (src/ipc/wails.ts and
// src/ipc/commands.ts) for stream channels, so a command input cannot receive them in its body. clientId is
// stripped too but is re-delivered to handlers through the envelope (ipc.Call.ClientID), so it is not reserved here.
var reservedTransportFields = map[string]bool{"channel": true}

// checkReservedInputFields rejects non-channel commands whose input declares a field the transport strips.
func checkReservedInputFields(commands []commandContract) error {
	for _, command := range commands {
		if command.Channel || command.Input == nil {
			continue
		}
		if field := reservedInputField(command.Input, 0); field != "" {
			return fmt.Errorf("command %s input declares JSON field %q, which the IPC transport reserves for stream channels and strips from the request body; rename the field", command.Name, field)
		}
	}
	return nil
}

func reservedInputField(value types.Type, depth int) string {
	if pointer, ok := types.Unalias(value).(*types.Pointer); ok {
		value = pointer.Elem()
	}
	structure, ok := types.Unalias(value).Underlying().(*types.Struct)
	if !ok || depth > 4 {
		return ""
	}
	for index := 0; index < structure.NumFields(); index++ {
		field := structure.Field(index)
		if !field.Exported() {
			continue
		}
		tag := strings.Split(reflect.StructTag(structure.Tag(index)).Get("json"), ",")[0]
		if tag == "-" {
			continue
		}
		if field.Embedded() && tag == "" {
			if nested := reservedInputField(field.Type(), depth+1); nested != "" {
				return nested
			}
			continue
		}
		if tag == "" {
			tag = field.Name()
		}
		if reservedTransportFields[tag] {
			return tag
		}
	}
	return ""
}
