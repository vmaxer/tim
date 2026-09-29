package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// CompileTim compiles the program in inputPath to an executable.
func CompileTim(inputPath string, outputPath string, platform Platform) error {
	return CompileTimWithOptions(inputPath, outputPath, platform, 0, VerboseMode, false)
}

// CompileTimWithOptions compiles with the given verbosity; depsOnly lists the
// program's imports instead of compiling it.
func CompileTimWithOptions(inputPath string, outputPath string, platform Platform, _ float64, verbose bool, depsOnly bool) error {
	if verbose {
		old := VerboseMode
		VerboseMode = true
		defer func() { VerboseMode = old }()
	}
	path, err := filepath.Abs(inputPath)
	if err != nil {
		return err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read %s: %v", inputPath, err)
	}
	if depsOnly {
		prog := parseQuietly(string(src), path)
		if prog == nil {
			return fmt.Errorf("%s does not parse", inputPath)
		}
		for _, s := range prog.Statements {
			switch s := s.(type) {
			case *ImportStmt:
				fmt.Printf("%s as %s\n", s.URL, s.Alias)
			case *CImportStmt:
				fmt.Printf("C %s as %s\n", s.Library, s.Alias)
			}
		}
		return nil
	}
	handled, err := tryCore(src, path, outputPath, platform)
	if !handled && err == nil {
		err = fmt.Errorf("%s: cannot compile for %s", inputPath, platform.FullString())
	}
	return err
}

// tryCore compiles with the core code generator. It returns handled=false
// when it cannot compile the program for the platform.
func tryCore(src []byte, path, out string, p Platform) (handled bool, err error) {
	why := ""
	defer func() {
		if VerboseMode && !handled {
			fmt.Fprintf(os.Stderr, "tim: %s\n", why)
		}
	}()
	t, a, write := coreTargetFor(p)
	if a == nil {
		why = "platform " + p.FullString()
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
	if err := loadModules(prog, path, p); err != nil {
		return true, err
	}
	addPrelude(prog)
	c, err := Check(prog, path, string(src))
	// A program may span the .tim files of its directory: take definitions
	// of undefined names from them until nothing more resolves.
	for range 8 {
		var ce *CheckError
		if !errors.As(err, &ce) || !ce.OnlyUndefined || !addSiblingDefs(prog, path, ce.Undefined) {
			break
		}
		c, err = Check(prog, path, string(src))
	}
	if err != nil {
		ce := err.(*CheckError)
		fmt.Fprint(os.Stderr, ce.Color)
		return true, newReportedError(ce.Plain)
	}
	if len(c.Unsupported) > 0 {
		return true, fmt.Errorf("%s uses what Tim 2 does not support: %s", filepath.Base(path), strings.Join(c.Unsupported, ", "))
	}
	code, entry, cimps, err := compileCore(c, a, t)
	if err != nil {
		return true, fmt.Errorf("internal compiler error: %v", err)
	}
	return true, write(out, p.Arch, code, entry, cimps)
}

type coreWriter func(path string, arch Arch, code []byte, entry int, cimps []cImport) error

// coreTargetFor returns what the core needs for a platform, or a nil asm.
func coreTargetFor(p Platform) (coreTarget, asm, coreWriter) {
	t := coreTarget{os: p.OS}
	switch {
	case p.OS == OSLinux && p.Arch == ArchX86_64:
		t.blob, t.syms, t.layout = rtLinuxAMD64, rtLinuxAMD64Syms, elfLayout
		return t, newX86(), writeCoreELF
	case p.OS == OSLinux && p.Arch == ArchARM64:
		t.blob, t.syms, t.layout = rtLinuxARM64, rtLinuxARM64Syms, elfLayout
		return t, newA64(), writeCoreELF
	case p.OS == OSLinux && p.Arch == ArchRiscv64:
		t.blob, t.syms, t.layout = rtLinuxRISCV64, rtLinuxRISCV64Syms, elfLayout
		return t, newRV(), writeCoreELF
	case p.OS == OSWindows && p.Arch == ArchX86_64:
		t.blob, t.syms, t.layout = rtWindowsAMD64, rtWindowsAMD64Syms, peLayout
		return t, newX86(), writeCorePE
	case p.OS == OSWindows && p.Arch == ArchARM64:
		t.blob, t.syms, t.layout = rtWindowsARM64, rtWindowsARM64Syms, peLayout
		return t, newA64(), writeCorePE
	case p.OS == OSDarwin && p.Arch == ArchARM64:
		t.blob, t.syms, t.layout = rtDarwinARM64, rtDarwinARM64Syms, machoLayout
		return t, newA64(), writeCoreMachO
	}
	return t, nil, nil
}

// addSiblingDefs adds the definitions of the given names from the other
// .tim files next to path, with those files' cstructs and C imports. It
// reports whether it added any.
func addSiblingDefs(prog *Program, path string, names []string) bool {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tim"))
	added := false
	for _, m := range matches {
		if filepath.Base(m) == filepath.Base(path) {
			continue
		}
		src, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		sib := parseQuietly(string(src), m)
		if sib == nil {
			continue
		}
		var defs, support []Statement
		for _, s := range sib.Statements {
			switch s := s.(type) {
			case *AssignStmt:
				if _, fn := s.Value.(*LambdaExpr); fn && !s.IsUpdate && want[s.Name] {
					want[s.Name] = false
					defs = append(defs, s)
				}
			case *CStructDecl, *CImportStmt:
				support = append(support, s)
			}
		}
		if len(defs) == 0 {
			continue
		}
		for _, s := range support {
			if cs, ok := s.(*CStructDecl); ok {
				if prog.CStructs[cs.Name] != nil {
					continue
				}
				if prog.CStructs == nil {
					prog.CStructs = map[string]*CStructDecl{}
				}
				prog.CStructs[cs.Name] = cs
			}
			prog.Statements = append([]Statement{s}, prog.Statements...)
		}
		prog.Statements = append(prog.Statements, defs...)
		added = true
	}
	return added
}

// parseQuietly parses a file, returning nil instead of reporting errors.
func parseQuietly(src, path string) (prog *Program) {
	defer func() {
		if recover() != nil {
			prog = nil
		}
	}()
	p := NewParserWithFilename(src, path)
	p.quiet = true
	return p.ParseProgramRaw()
}

// loadModules replaces each Tim import with the module's definitions:
// functions and globals named alias.name (unprefixed for `as *` or a module
// that says `export *`), with the module's cstructs and C imports.
func loadModules(prog *Program, path string, p Platform) error {
	var rest []Statement
	var mods []Statement
	for _, s := range prog.Statements {
		imp, ok := s.(*ImportStmt)
		if !ok {
			rest = append(rest, s)
			continue
		}
		src := imp.URL
		if strings.HasPrefix(src, ".") {
			src = filepath.Clean(filepath.Join(filepath.Dir(path), src))
		}
		files, err := ResolveImport(&ImportSpec{Source: src, Version: imp.Version, Alias: imp.Alias}, p.OS.String(), p.Arch.String())
		if err != nil {
			return fmt.Errorf("cannot import %s: %v", imp.URL, err)
		}
		self, _ := filepath.Abs(path)
		for _, f := range files {
			if abs, _ := filepath.Abs(f); abs == self {
				continue
			}
			text, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			mod := NewParserWithFilename(string(text), f).ParseProgramRaw()
			prefix := imp.Alias + "."
			if imp.Alias == "*" || mod.ExportMode == "*" {
				prefix = ""
			}
			renames := map[string]string{}
			for _, s := range mod.Statements {
				if a, ok := s.(*AssignStmt); ok && !a.IsUpdate {
					renames[a.Name] = prefix + a.Name
				}
			}
			for _, s := range mod.Statements {
				switch s := s.(type) {
				case *AssignStmt:
					if s.IsUpdate {
						continue
					}
					walkNodes(s, func(n any) {
						switch n := n.(type) {
						case *CallExpr:
							if to, ok := renames[n.Function]; ok {
								n.Function = to
							}
						case *IdentExpr:
							if to, ok := renames[n.Name]; ok {
								n.Name = to
							}
						}
					})
					s.Name = renames[s.Name]
					mods = append(mods, s)
				case *CStructDecl:
					if prog.CStructs == nil {
						prog.CStructs = map[string]*CStructDecl{}
					}
					prog.CStructs[s.Name] = s
					mods = append(mods, s)
				case *CImportStmt:
					mods = append(mods, s)
				}
			}
		}
	}
	prog.Statements = append(mods, rest...)
	return nil
}
