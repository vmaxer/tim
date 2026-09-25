package main

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
)

// The prelude defines the higher-order builtins in Tim.
var preludeDefs = map[string]string{
	"map":     "map(xs, f) = [f(x) @ x in xs]",
	"filter":  "filter(xs, f) = [x @ x in xs if f(x)]",
	"fold":    "fold(xs, init, f) = {\n acc := init\n @ x in xs { acc <- f(acc, x) }\n acc\n}",
	"any":     "any(xs, f) = {\n @ x in xs { f(x) { ret 1 } }\n 0\n}",
	"all":     "all(xs, f) = {\n @ x in xs { not f(x) { ret 0 } }\n 1\n}",
	"sort_by": "sort_by(xs, f) = __sort_keys(xs, [f(x) @ x in xs])",
}

// walkNodes calls visit for every node reachable from n.
func walkNodes(n any, visit func(any)) {
	v := reflect.ValueOf(n)
	if !v.IsValid() || (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil() {
		return
	}
	visit(n)
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !f.CanInterface() {
			continue
		}
		switch f.Kind() {
		case reflect.Interface, reflect.Pointer:
			if !f.IsNil() {
				walkNodes(f.Interface(), visit)
			}
		case reflect.Slice:
			for j := 0; j < f.Len(); j++ {
				e := f.Index(j)
				switch e.Kind() {
				case reflect.Interface, reflect.Pointer:
					walkNodes(e.Interface(), visit)
				case reflect.Struct:
					walkNodes(e.Addr().Interface(), visit)
				}
			}
		}
	}
}

// addPrelude adds the prelude functions a program uses and does not define.
func addPrelude(prog *Program) {
	defined := map[string]bool{}
	for _, s := range prog.Statements {
		if a, ok := s.(*AssignStmt); ok && !a.IsUpdate {
			defined[a.Name] = true
		}
	}
	used := map[string]bool{}
	walkNodes(prog, func(n any) {
		switch n := n.(type) {
		case *CallExpr:
			if n.Function == "sort" && len(n.Args) == 2 && !defined["sort"] {
				n.Function = "sort_by"
			}
			used[n.Function] = true
		case *IdentExpr:
			used[n.Name] = true
		}
	})
	var src []string
	for name, def := range preludeDefs {
		if used[name] && !defined[name] {
			src = append(src, def)
		}
	}
	if len(src) == 0 {
		return
	}
	p := NewParserWithFilename(strings.Join(src, "\n"), "<prelude>")
	extra := p.ParseProgramRaw()
	prog.Statements = append(prog.Statements, extra.Statements...)
}

// tryCore compiles with the core code generator. It returns handled=false
// when the program needs the legacy backend.
func tryCore(src []byte, path, out string, p Platform) (handled bool, err error) {
	why := ""
	defer func() {
		if VerboseMode && !handled {
			fmt.Fprintf(os.Stderr, "core: legacy backend (%s)\n", why)
		}
	}()
	if os.Getenv("TIM_LEGACY") != "" || p.OS != OSLinux {
		why = "requested or not Linux"
		return false, nil
	}
	var blob []byte
	var syms map[string]int
	var a asm
	switch p.Arch {
	case ArchX86_64:
		blob, syms, a = rtLinuxAMD64, rtLinuxAMD64Syms, newX86()
	case ArchARM64:
		blob, syms, a = rtLinuxARM64, rtLinuxARM64Syms, newA64()
	case ArchRiscv64:
		blob, syms, a = rtLinuxRISCV64, rtLinuxRISCV64Syms, newRV()
	default:
		why = "architecture"
		return false, nil
	}
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok && errors.Is(e, ErrAlreadyReported) {
				handled, err = true, e
				return
			}
			panic(r)
		}
	}()
	prog := NewParserWithFilename(string(src), path).ParseProgramRaw()
	for _, s := range prog.Statements {
		switch s.(type) {
		case *ImportStmt, *CImportStmt:
			why = "imports"
			return false, nil
		}
	}
	addPrelude(prog)
	c, err := Check(prog, path, string(src))
	if err != nil {
		var ce *CheckError
		if errors.As(err, &ce) && ce.OnlyUndefined {
			why = "undefined names, perhaps defined by a sibling file"
			return false, nil
		}
		fmt.Fprint(os.Stderr, ce.Color)
		return true, newReportedError(ce.Plain)
	}
	if len(c.Unsupported) > 0 {
		why = strings.Join(c.Unsupported, ", ")
		return false, nil
	}
	code, entry, err := compileCore(c, a, blob, syms)
	if err != nil {
		why = err.Error()
		return false, nil
	}
	return true, writeCoreELF(out, p.Arch, code, entry)
}
