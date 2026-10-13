package main

// check-conflict-columns verifies that in ON CONFLICT upserts the table name
// arguments of conflictTargetColumn / conflictOwnerColumn match the target
// model table of Create(&T) in the same statement.
// A mismatch surfaces on PostgreSQL as a 42702/42P01 runtime error; there
// have been two precedents.
//
// Forms with dynamically resolved table names (stmt.Schema.Table) are exempt.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func modelTables(fset *token.FileSet, dir string) (map[string]string, error) {
	tables := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Name.Name != "TableName" {
				continue
			}
			var recvType string
			switch expr := fn.Recv.List[0].Type.(type) {
			case *ast.Ident:
				recvType = expr.Name
			case *ast.StarExpr:
				if ident, ok := expr.X.(*ast.Ident); ok {
					recvType = ident.Name
				}
			}
			if recvType == "" || fn.Body == nil {
				continue
			}
			for _, stmt := range fn.Body.List {
				ret, ok := stmt.(*ast.ReturnStmt)
				if !ok || len(ret.Results) != 1 {
					continue
				}
				if lit, ok := ret.Results[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					tables[recvType] = strings.Trim(lit.Value, `"`)
				}
			}
		}
	}
	return tables, nil
}

var simpleTypes = map[string]bool{
	"row": true, "item": true, "value": true, "values": true, "entry": true, "tombstone": true,
}

func main() {
	fset := token.NewFileSet()
	tables, err := modelTables(fset, "internal/store/model")
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-conflict-columns: 读取模型表名失败:", err)
		os.Exit(1)
	}
	failures := 0
	unresolved := 0
	err = filepath.WalkDir("internal", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(src)
		for _, marker := range []string{"conflictTargetColumn(", "conflictOwnerColumn("} {
			argIndex := 0
			if marker == "conflictOwnerColumn(" {
				argIndex = 1
			}
			for offset := 0; ; {
				idx := strings.Index(text[offset:], marker)
				if idx < 0 {
					break
				}
				start := offset + idx + len(marker)
				offset = start
				table := nthStringArg(text, start, argIndex)
				if table == "" || table == "__dynamic__" {
					continue
				}
				line := 1 + strings.Count(text[:start], "\n")
				target := createTargetType(text, start)
				if target == "" {
					unresolved++
					fmt.Printf("unresolved: %s:%d (表名 %q) 无法静态确定 Create 目标, 请人工核对\n", path, line, table)
					continue
				}
				want := tables[target]
				if want == "" {
					unresolved++
					fmt.Printf("unresolved: %s:%d Create 目标 %s 不在模型表(表名 %q), 请人工核对\n", path, line, target, table)
					continue
				}
				if want != table {
					failures++
					fmt.Printf("mismatch: %s:%d 限定表 %q 与 Create 目标 %s(表 %q) 不一致\n", path, line, table, target, want)
				}
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-conflict-columns:", err)
		os.Exit(1)
	}
	if failures > 0 {
		fmt.Printf("check-conflict-columns: %d 处表名错位\n", failures)
		os.Exit(1)
	}
	fmt.Printf("check-conflict-columns: 全部一致(%d 处无法静态确定需人工留意)\n", unresolved)
}

// nthStringArg returns the n-th string literal argument of a call, or "" when
// not found.
func nthStringArg(text string, start, want int) string {
	// Dynamic resolution (stmt.Schema.Table) is not a string argument; return a
	// sentinel for the caller to exempt.
	if strings.HasPrefix(text[start:], "stmt.Schema.Table") {
		return "__dynamic__"
	}
	depth := 0
	seen := 0
	for i := start; i < len(text); i++ {
		c := text[i]
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return ""
			}
		case '"', '`':
			if depth != 0 {
				continue
			}
			end := i + 1
			for end < len(text) && text[end] != c {
				if text[end] == '\\' && c == '"' {
					end++
				}
				end++
			}
			if seen == want {
				return text[i+1 : end]
			}
			seen++
			i = end
		}
	}
	return ""
}

// createTargetType finds the Create(&T) target type of the same statement by
// scanning back from the conflict marker position.
func createTargetType(text string, from int) string {
	window := text[from:min(len(text), from+3000)]
	idx := indexCreate(window)
	if idx < 0 {
		return ""
	}
	rest := window[idx:]
	end := strings.IndexAny(rest, "{},\n)")
	if end < 0 {
		return ""
	}
	name := strings.TrimSpace(rest[:end])
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[dot+1:]
	}
	if simpleTypes[name] {
		return declareType(text[:from], name)
	}
	return name
}

// indexCreate finds the first Create( call (ignoring whitespace and newlines)
// and returns the start of its first argument expression.
func indexCreate(window string) int {
	idx := strings.Index(window, "Create(&")
	if idx < 0 {
		return -1
	}
	return idx + len("Create(&")
}

// declareType traces back the model type declaration of a simple variable
// name within the same function body.
func declareType(prefix, name string) string {
	fnStart := strings.LastIndex(prefix, "\nfunc ")
	if fnStart < 0 {
		return ""
	}
	body := prefix[fnStart:]
	type tail struct {
		typ string
	}
	var found *tail
	// Parameters and local declarations: "name store.X" / "name model.X" / "name X{Big}"
	for _, decl := range strings.Split(body, "\n") {
		idx := strings.Index(decl, name+" ")
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(decl[idx+len(name)+1:])
		rest = strings.TrimLeft(rest, ":= ")
		rest = strings.TrimPrefix(rest, "store.")
		rest = strings.TrimPrefix(rest, "model.")
		end := strings.IndexAny(rest, ",) ={\t")
		if end <= 0 {
			continue
		}
		candidate := rest[:end]
		if candidate == "" || strings.ContainsAny(candidate, `"'`+"`") {
			continue
		}
		found = &tail{typ: candidate}
	}
	if found != nil {
		return found.typ
	}
	// Assignment declarations: "name := X{" / "name := store.X{"
	needle := name + " := "
	if idx := strings.LastIndex(body, needle); idx >= 0 {
		rest := strings.TrimPrefix(strings.TrimSpace(body[idx+len(needle):]), "store.")
		rest = strings.TrimPrefix(rest, "model.")
		if end := strings.IndexAny(rest, "{\n"); end > 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
