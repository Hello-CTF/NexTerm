package main

import (
	"fmt"
	"go/types"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type typeRef struct {
	Go       string
	TS       string
	Manifest string
}

type tsDeclaration struct {
	Name string
	Body string
}

type typeRegistry struct {
	declarations map[string]*tsDeclaration
	names        map[string]string
}

var tsNameOverrides = map[string]string{
	"github.com/Hello-CTF/NexTerm/internal/app/production.fsCancelRequest":           "FSCancelRequest",
	"github.com/Hello-CTF/NexTerm/internal/app/production.fsChunkUploadBeginDTO":     "FSChunkUploadBeginDTO",
	"github.com/Hello-CTF/NexTerm/internal/app/production.fsChunkUploadProgressDTO":  "FSChunkUploadProgressDTO",
	"github.com/Hello-CTF/NexTerm/internal/app/production.fsEntryDTO":                "FSEntryDTO",
	"github.com/Hello-CTF/NexTerm/internal/app/production.fsRangeReadDTO":            "FSRangeReadDTO",
	"github.com/Hello-CTF/NexTerm/internal/app/production.fsReadDTO":                 "FSReadDTO",
	"github.com/Hello-CTF/NexTerm/internal/app/production.fsRootDTO":                 "FSRootDTO",
	"github.com/Hello-CTF/NexTerm/internal/app/production.fsRootsDTO":                "FSRootsDTO",
	"github.com/Hello-CTF/NexTerm/internal/app/production.groupDTO":                  "AssetGroupDTO",
	"github.com/Hello-CTF/NexTerm/internal/app/production.groupInputRequest":         "AssetGroupInputRequest",
	"github.com/Hello-CTF/NexTerm/internal/app/production.groupPatchRequest":         "AssetGroupPatchRequest",
	"github.com/Hello-CTF/NexTerm/internal/app/production.idRequest":                 "IDRequest",
	"github.com/Hello-CTF/NexTerm/internal/app/production.localFileReadRangeRequest": "LocalFileReadRangeRequest",
	"github.com/Hello-CTF/NexTerm/internal/app/production.localFileReadRequest":      "LocalFileReadRequest",
	"github.com/Hello-CTF/NexTerm/internal/app/production.localFolderFileDTO":        "LocalFolderFileDTO",
	"github.com/Hello-CTF/NexTerm/internal/app/production.localFolderListDTO":        "LocalFolderListDTO",
	"github.com/Hello-CTF/NexTerm/internal/app/production.localFolderListRequest":    "LocalFolderListRequest",
	"github.com/Hello-CTF/NexTerm/internal/ai/cron.Job":                              "CronJob",
	"github.com/Hello-CTF/NexTerm/internal/ai/cron.Lease":                            "CronLease",
	"github.com/Hello-CTF/NexTerm/internal/ai/cron.RunState":                         "CronRunState",
	"github.com/Hello-CTF/NexTerm/internal/execbatch.Request":                        "ExecBatchRequest",
	"github.com/Hello-CTF/NexTerm/internal/execbatch.Batch":                          "ExecBatch",
	"github.com/Hello-CTF/NexTerm/internal/execbatch.Result":                         "ExecBatchResult",
	"github.com/Hello-CTF/NexTerm/internal/execbatch.CancelResult":                   "ExecBatchCancelResult",
	"github.com/Hello-CTF/NexTerm/internal/forward.Rule":                             "ForwardRule",
	"github.com/Hello-CTF/NexTerm/internal/forward.ruleRequest":                      "ForwardRuleRequest",
	"github.com/Hello-CTF/NexTerm/internal/forward.startRuleRequest":                 "ForwardRuleStartRequest",
	"github.com/Hello-CTF/NexTerm/internal/logtail.Request":                          "LogTailRequest",
	"github.com/Hello-CTF/NexTerm/internal/logtail.Source":                           "LogTailSource",
	"github.com/Hello-CTF/NexTerm/internal/logtail.StartResult":                      "LogTailStartResult",
	"github.com/Hello-CTF/NexTerm/internal/logtail.CancelResult":                     "LogTailCancelResult",
	"github.com/Hello-CTF/NexTerm/internal/mount.CreateArgs":                         "MountCreateArgs",
	"github.com/Hello-CTF/NexTerm/internal/mount.Entry":                              "MountEntryDTO",
	"github.com/Hello-CTF/NexTerm/internal/mount.RemoveArgs":                         "MountRemoveArgs",
	"github.com/Hello-CTF/NexTerm/internal/mount.listRequest":                        "MountListRequest",
	"github.com/Hello-CTF/NexTerm/internal/store.ForwardRuleInput":                   "ForwardRuleInput",
	"github.com/Hello-CTF/NexTerm/internal/biometric.Status":                         "BiometricStatus",
	"github.com/Hello-CTF/NexTerm/internal/update.Result":                            "UpdateResult",
	"github.com/Hello-CTF/NexTerm/internal/update.Status":                            "UpdateStatus",
	"github.com/Hello-CTF/NexTerm/internal/vault.Status":                             "VaultStatus",
}

var consumerOptionalRequestFields = map[string]map[string]bool{
	"github.com/Hello-CTF/NexTerm/internal/app/production.assetInputRequest": {
		"GroupID": true, "Host": true, "Port": true, "Username": true, "AuthKind": true,
		"KeyPath": true, "CredID": true, "Options": true, "Tags": true, "Note": true, "Sort": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.assetPatchRequest": {
		"Name": true, "Options": true, "Tags": true, "Note": true, "Sort": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.assetProbeBatchRequest": {
		"TimeoutMS": true, "MaxConcurrent": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.credentialSetRequest": {
		"ID": true, "Source": true, "Passphrase": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.credentialUpdateRequest": {
		"Name": true, "Secret": true, "Source": true, "Passphrase": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.groupInputRequest": {
		"ParentID": true, "Sort": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.groupPatchRequest": {
		"Name": true, "Sort": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.idRequest": {
		"ID": true, "Q": true, "Name": true, "Body": true, "Path": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.knownHostRequest": {
		"Key": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.localFileReadRangeRequest": {
		"MaxBytes": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.localFileReadRequest": {
		"MaxBytes": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.auditQueryRequest": {
		"SessionID": true, "AssetID": true, "Source": true, "Kind": true, "Limit": true, "Offset": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.commandLogQueryRequest": {
		"SessionID": true, "AssetID": true, "UserID": true, "Limit": true, "Offset": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.snippetInputRequest": {
		"GroupID": true, "Sort": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.snippetPatchRequest": {
		"Sort": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.sshImportPreviewRequest": {
		"Path": true, "Confirmed": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.sshImportApplyRequest": {
		"Path": true, "Confirmed": true, "GroupID": true, "Tags": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.transcriptReadRequest": {
		"MaxBytes": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.vaultGenerateKeyRequest": {
		"Algorithm": true, "Passphrase": true,
	},
	"github.com/Hello-CTF/NexTerm/internal/app/production.vaultPasswordRequest": {
		"Password": true, "OldPassword": true, "NewPassword": true,
	},
}

type consumerRequestField struct {
	name     string
	optional bool
	nullable bool
}

var consumerCommandRequestProjections = map[string][]consumerRequestField{
	"app_update_install": {{name: "Version", nullable: true}},
	"audit_count": {
		{name: "SessionID", optional: true}, {name: "AssetID", optional: true},
		{name: "Source", optional: true}, {name: "Kind", optional: true},
	},
	"command_count": {
		{name: "SessionID", optional: true}, {name: "AssetID", optional: true}, {name: "UserID", optional: true},
	},
	"db_query":                        {{name: "ConnID"}, {name: "SQL"}, {name: "TimeoutMS", optional: true}},
	"docker_action":                   {{name: "SessionID"}, {name: "ContainerID"}, {name: "Action"}, {name: "NewName", optional: true}},
	"docker_compose_redeploy_attach":  {{name: "SessionID"}, {name: "Project"}, {name: "Path"}, {name: "Ticket"}},
	"docker_compose_redeploy_prepare": {{name: "SessionID"}, {name: "Project"}, {name: "Path"}, {name: "BaseContentBase64"}, {name: "ContentBase64"}, {name: "ProductionConfirmation"}},
	"docker_container_list_dir":       {{name: "SessionID"}, {name: "ContainerID"}, {name: "Path"}},
	"docker_exec_attach":              {{name: "SessionID"}, {name: "ContainerID"}, {name: "Cmd", optional: true}, {name: "Cols"}, {name: "Rows"}, {name: "ClientID"}},
	"docker_image_pull":               {{name: "SessionID"}, {name: "Image"}},
	"docker_image_remove":             {{name: "SessionID"}, {name: "Image"}, {name: "Force", optional: true}},
	"docker_inspect":                  {{name: "SessionID"}, {name: "ContainerID"}},
	"fs_checksum":                     {{name: "SessionID"}, {name: "Path"}, {name: "Algo", optional: true}},
	"fs_chmod":                        {{name: "SessionID"}, {name: "Path"}, {name: "Mode"}},
	"fs_delete":                       {{name: "SessionID"}, {name: "Path"}, {name: "IsDir"}},
	"fs_download":                     {{name: "SessionID"}, {name: "RemotePath"}, {name: "LocalPath"}, {name: "Resume", optional: true}, {name: "TaskID", optional: true}, {name: "AttemptID", optional: true}},
	"fs_extract":                      {{name: "SessionID"}, {name: "Path"}},
	"fs_list":                         {{name: "SessionID"}, {name: "Path"}},
	"fs_mkdir":                        {{name: "SessionID"}, {name: "Path"}},
	"fs_pack_download":                {{name: "SessionID"}, {name: "RemotePath"}, {name: "LocalPath"}, {name: "TaskID", optional: true}, {name: "AttemptID", optional: true}},
	"fs_read":                         {{name: "SessionID"}, {name: "Path"}, {name: "MaxBytes", optional: true}},
	"fs_read_range":                   {{name: "SessionID"}, {name: "Path"}, {name: "Offset"}, {name: "MaxBytes", optional: true}},
	"fs_rename":                       {{name: "SessionID"}, {name: "From"}, {name: "To"}},
	"fs_roots":                        {{name: "SessionID"}},
	"fs_upload":                       {{name: "SessionID"}, {name: "LocalPath"}, {name: "RemotePath"}, {name: "Resume", optional: true}, {name: "TaskID", optional: true}, {name: "AttemptID", optional: true}},
	"fs_upload_abort":                 {{name: "SessionID"}, {name: "UploadID"}, {name: "TaskID"}, {name: "AttemptID"}},
	"fs_upload_begin":                 {{name: "SessionID"}, {name: "RemotePath"}, {name: "TotalSize"}, {name: "ChunkSize", optional: true}, {name: "TaskID"}, {name: "AttemptID"}},
	"fs_upload_chunk":                 {{name: "SessionID"}, {name: "UploadID"}, {name: "TaskID"}, {name: "AttemptID"}, {name: "Index"}, {name: "Size"}, {name: "SHA256"}, {name: "ContentBase64"}},
	"fs_upload_commit":                {{name: "SessionID"}, {name: "UploadID"}, {name: "TaskID"}, {name: "AttemptID"}},
	"fs_write":                        {{name: "SessionID"}, {name: "Path"}, {name: "ContentBase64"}, {name: "Backup"}, {name: "TaskID", optional: true}, {name: "AttemptID", optional: true}},
	"snippet_delete":                  {{name: "ID"}},
}

func projectConsumerCommandInput(command string, value types.Type) (types.Type, error) {
	projection, ok := consumerCommandRequestProjections[command]
	if !ok {
		return value, nil
	}
	structure, ok := types.Unalias(value).Underlying().(*types.Struct)
	if !ok {
		return nil, fmt.Errorf("request projection requires a struct, got %s", typeIdentifier(value))
	}
	fields := make([]*types.Var, 0, len(projection))
	tags := make([]string, 0, len(projection))
	seen := make(map[string]bool)
	for _, projected := range projection {
		if seen[projected.name] {
			return nil, fmt.Errorf("request projection field %s is declared twice", projected.name)
		}
		seen[projected.name] = true
		index := -1
		for candidate := 0; candidate < structure.NumFields(); candidate++ {
			if structure.Field(candidate).Name() == projected.name {
				index = candidate
				break
			}
		}
		if index < 0 {
			return nil, fmt.Errorf("request projection field %s is missing from %s", projected.name, typeIdentifier(value))
		}
		tag := structure.Tag(index)
		if projected.optional {
			var err error
			tag, err = optionalConsumerFieldTag(tag)
			if err != nil {
				return nil, fmt.Errorf("request projection field %s: %w", projected.name, err)
			}
		}
		field := structure.Field(index)
		if projected.nullable {
			field = types.NewVar(field.Pos(), field.Pkg(), field.Name(), types.NewPointer(field.Type()))
		}
		fields = append(fields, field)
		tags = append(tags, tag)
	}
	return types.NewStruct(fields, tags), nil
}

func optionalConsumerFieldTag(tag string) (string, error) {
	jsonTag := reflect.StructTag(tag).Get("json")
	parts := strings.Split(jsonTag, ",")
	if parts[0] == "" {
		return "", fmt.Errorf("optional field requires an explicit JSON name")
	}
	for _, option := range parts[1:] {
		if option == "omitempty" || option == "omitzero" {
			return tag, nil
		}
	}
	original := `json:"` + jsonTag + `"`
	if !strings.Contains(tag, original) {
		return "", fmt.Errorf("cannot update JSON tag %q", tag)
	}
	replacement := `json:"` + strings.Join(append(parts, "omitempty"), ",") + `"`
	return strings.Replace(tag, original, replacement, 1), nil
}

func newTypeRegistry() *typeRegistry {
	return &typeRegistry{declarations: make(map[string]*tsDeclaration), names: make(map[string]string)}
}

func (r *typeRegistry) ref(value types.Type) (typeRef, error) {
	ts, _, err := r.mapType(value, "")
	if err != nil {
		return typeRef{}, err
	}
	manifest, _, err := r.mapType(value, "contractTypes.")
	if err != nil {
		return typeRef{}, err
	}
	return typeRef{Go: typeIdentifier(value), TS: ts, Manifest: manifest}, nil
}

func (r *typeRegistry) mapType(value types.Type, namespace string) (string, bool, error) {
	value = types.Unalias(value)
	switch current := value.(type) {
	case *types.Basic:
		if current.Kind() == types.Invalid {
			return "", false, fmt.Errorf("invalid Go type in selected IPC contract")
		}
		return basicType(current), false, nil
	case *types.Pointer:
		mapped, _, err := r.mapType(current.Elem(), namespace)
		return unionWithNull(mapped), false, err
	case *types.Slice:
		if isByteSlice(current) {
			return "string | number[]", false, nil
		}
		mapped, _, err := r.mapType(current.Elem(), namespace)
		return arrayType(mapped), false, err
	case *types.Array:
		mapped, _, err := r.mapType(current.Elem(), namespace)
		return arrayType(mapped), false, err
	case *types.Map:
		key, ok := types.Unalias(current.Key()).(*types.Basic)
		if !ok || key.Kind() != types.String {
			return "unknown", false, nil
		}
		mapped, _, err := r.mapType(current.Elem(), namespace)
		return "Record<string, " + mapped + ">", false, err
	case *types.Struct:
		body, err := r.structBody(current, namespace, "")
		return body, false, err
	case *types.Named:
		if isTime(current) {
			return "string", false, nil
		}
		if isRawMessage(current) {
			return "unknown", false, nil
		}
		if isRedisScanResult(current) {
			return "[number, string[]]", false, nil
		}
		if wireType, ok := optionalIPCWireType(current); ok {
			mapped, _, err := r.mapType(wireType, namespace)
			return unionWithNull(mapped), false, err
		}
		if structure, ok := types.Unalias(current.Underlying()).(*types.Struct); ok {
			name, err := r.namedStruct(current, structure)
			return namespace + name, true, err
		}
		mapped, _, err := r.mapType(current.Underlying(), namespace)
		return mapped, false, err
	default:
		return "unknown", false, nil
	}
}

func optionalIPCWireType(value types.Type) (types.Type, bool) {
	named, ok := types.Unalias(value).(*types.Named)
	if !ok || named.Obj().Pkg() == nil ||
		named.Obj().Pkg().Path() != "github.com/Hello-CTF/NexTerm/internal/app/production" ||
		named.Obj().Name() != "optionalIPCValue" || named.TypeArgs().Len() != 1 {
		return nil, false
	}
	return named.TypeArgs().At(0), true
}

func (r *typeRegistry) namedStruct(named *types.Named, structure *types.Struct) (string, error) {
	identifier := typeIdentifier(named)
	if declaration, exists := r.declarations[identifier]; exists {
		return declaration.Name, nil
	}
	name := tsNameOverrides[identifier]
	if name == "" {
		name = exportedName(named.Obj().Name())
	}
	if owner, exists := r.names[name]; exists && owner != identifier {
		return "", fmt.Errorf("TypeScript name %q is used by both %s and %s", name, owner, identifier)
	}
	declaration := &tsDeclaration{Name: name, Body: "unknown"}
	r.declarations[identifier] = declaration
	r.names[name] = identifier
	body, err := r.structBody(structure, "", identifier)
	if err != nil {
		return "", err
	}
	declaration.Body = body
	return name, nil
}

func (r *typeRegistry) structBody(structure *types.Struct, namespace, owner string) (string, error) {
	if structure.NumFields() == 0 {
		return "Record<string, never>", nil
	}
	var embedded []string
	var fields []string
	for index := 0; index < structure.NumFields(); index++ {
		field := structure.Field(index)
		if !field.Exported() {
			continue
		}
		tag := reflect.StructTag(structure.Tag(index)).Get("json")
		if tag == "-" {
			continue
		}
		if field.Embedded() && tag == "" {
			named, ok := types.Unalias(field.Type()).(*types.Named)
			if !ok {
				return "unknown", nil
			}
			if _, ok := types.Unalias(named.Underlying()).(*types.Struct); !ok {
				return "unknown", nil
			}
			mapped, _, err := r.mapType(named, namespace)
			if err != nil {
				return "", err
			}
			embedded = append(embedded, mapped)
			continue
		}
		if field.Embedded() && strings.Split(tag, ",")[0] == "" {
			return "unknown", nil
		}
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "" {
			name = field.Name()
		}
		mapped, _, err := r.mapType(field.Type(), namespace)
		if err != nil {
			return "", err
		}
		optional := consumerOptionalRequestFields[owner][field.Name()]
		if _, ok := optionalIPCWireType(field.Type()); ok {
			optional = true
		}
		stringEncoded := false
		for _, option := range parts[1:] {
			switch option {
			case "omitempty", "omitzero":
				optional = true
			case "string":
				stringEncoded = true
			}
		}
		if stringEncoded && (mapped == "number" || mapped == "boolean" || mapped == "string") {
			mapped = "string"
		}
		separator := ":"
		if optional {
			separator = "?:"
		}
		fields = append(fields, "  "+propertyName(name)+separator+" "+mapped+";")
	}
	body := "Record<string, never>"
	if len(fields) > 0 {
		body = "{\n" + strings.Join(fields, "\n") + "\n}"
	}
	if len(embedded) > 0 {
		if len(fields) == 0 {
			return strings.Join(embedded, " & "), nil
		}
		return strings.Join(append(embedded, body), " & "), nil
	}
	return body, nil
}

func (r *typeRegistry) render() string {
	declarations := make([]*tsDeclaration, 0, len(r.declarations))
	for _, declaration := range r.declarations {
		declarations = append(declarations, declaration)
	}
	sort.Slice(declarations, func(i, j int) bool { return declarations[i].Name < declarations[j].Name })
	var output strings.Builder
	output.WriteString("// Code generated by go run ./scripts/gen-ipc-contract; DO NOT EDIT.\n\n")
	for _, declaration := range declarations {
		fmt.Fprintf(&output, "export type %s = %s;\n\n", declaration.Name, declaration.Body)
	}
	return strings.TrimRight(output.String(), "\n") + "\n"
}

func typeIdentifier(value types.Type) string {
	return types.TypeString(types.Unalias(value), func(pkg *types.Package) string {
		if pkg == nil {
			return ""
		}
		return pkg.Path()
	})
}

func basicType(value *types.Basic) string {
	switch value.Kind() {
	case types.Bool:
		return "boolean"
	case types.String:
		return "string"
	case types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
		types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64, types.Uintptr,
		types.Float32, types.Float64:
		return "number"
	default:
		return "unknown"
	}
}

func isTime(named *types.Named) bool {
	return named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "time" && named.Obj().Name() == "Time"
}

func isRawMessage(named *types.Named) bool {
	return named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "encoding/json" && named.Obj().Name() == "RawMessage"
}

func isRedisScanResult(named *types.Named) bool {
	return named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "github.com/Hello-CTF/NexTerm/internal/db" && named.Obj().Name() == "RedisScanResult"
}

func isByteSlice(slice *types.Slice) bool {
	element, ok := types.Unalias(slice.Elem()).(*types.Basic)
	return ok && element.Kind() == types.Uint8
}

func arrayType(element string) string {
	if strings.Contains(element, " | ") {
		return "(" + element + ")[]"
	}
	return element + "[]"
}

func unionWithNull(value string) string {
	if value == "unknown" || value == "null" {
		return value
	}
	return value + " | null"
}

func exportedName(value string) string {
	for index, character := range value {
		return string(unicode.ToUpper(character)) + value[index+len(string(character)):]
	}
	return "Anonymous"
}

func propertyName(value string) string {
	if value != "" && (value[0] == '_' || value[0] == '$' || unicode.IsLetter(rune(value[0]))) {
		valid := true
		for _, character := range value[1:] {
			if character != '_' && character != '$' && !unicode.IsLetter(character) && !unicode.IsDigit(character) {
				valid = false
				break
			}
		}
		if valid {
			return value
		}
	}
	return strconv.Quote(value)
}
