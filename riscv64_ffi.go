package main

import (
	"debug/elf"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strings"
)

// C FFI. A program that calls C is linked dynamically: ld.so loads the
// libraries named in .dynamic and fills one GOT slot per imported function
// (eager R_RISCV_64 relocations), and calls go through those slots with the
// lp64d calling convention.

type rvFFI struct {
	imports   map[string]string
	constants map[string]*CHeaderConstants
}

type rvImport struct {
	sym  string
	slot *rvLabel
}

// usesFFI reports whether the program calls into C.
func rvUsesFFI(program *Program, ffi *rvFFI) bool {
	found := false
	astVisit(&program.Statements, true, func(n any) {
		switch n := n.(type) {
		case *CImportStmt:
			found = true
		case *CallExpr:
			if n.IsCFFI {
				found = true
			}
		}
	})
	return found
}

func (g *rvGen) importSlot(sym string) *rvLabel {
	for _, im := range g.imports {
		if im.sym == sym {
			return im.slot
		}
	}
	l := g.newLabel()
	g.imports = append(g.imports, rvImport{sym, l})
	return l
}

// callC calls an imported function through its GOT slot.
func (g *rvGen) callC(sym string) {
	g.la(rvT0, g.importSlot(sym))
	g.ld(rvT0, rvT0, 0)
	g.jalr(rvRA, rvT0, 0)
}

// cFunction resolves a call to C: c.f(...) (IsCFFI) or alias.f(...).
func (g *rvGen) cFunction(c *CallExpr) (sym string, sig *CFunctionSignature, ok bool) {
	if g.ffi == nil {
		return "", nil, false
	}
	alias := ""
	sym = c.Function
	if !c.IsCFFI {
		a, f, found := strings.Cut(c.Function, ".")
		if !found {
			return "", nil, false
		}
		if _, isC := g.ffi.imports[a]; !isC {
			return "", nil, false
		}
		alias, sym = a, f
	}
	if consts := g.ffi.constants[alias]; consts != nil {
		sig = consts.Functions[sym]
	}
	if sig == nil {
		sig = libcSignature(sym)
	}
	return sym, sig, true
}

type rvCKind int

const (
	rvCInt rvCKind = iota
	rvCUnsigned32
	rvCDouble
	rvCFloat
	rvCPointer
	rvCString
	rvCVoid
	rvCVariadicNumber
)

// rvFormatKinds classifies the variadic arguments of printf-family calls
// from a literal format string, by argument index.
func rvFormatKinds(sym string, sig *CFunctionSignature, args []Expression) map[int]rvCKind {
	fmtIndex := map[string]int{"printf": 0, "dprintf": 1, "fprintf": 1, "sprintf": 1, "snprintf": 2,
		"scanf": 0, "sscanf": 1, "SDL_Log": 0, "SDL_snprintf": 2}[sym]
	if _, known := map[string]bool{"printf": true, "dprintf": true, "fprintf": true, "sprintf": true,
		"snprintf": true, "SDL_Log": true, "SDL_snprintf": true}[sym]; !known || fmtIndex >= len(args) {
		return nil
	}
	f, ok := args[fmtIndex].(*StringExpr)
	if !ok {
		return nil
	}
	format := f.Value
	kinds := map[int]rvCKind{}
	next := fmtIndex + 1
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		i++
		for i < len(format) && strings.IndexByte("-+ #0123456789.lhLjzt*", format[i]) >= 0 {
			if format[i] == '*' {
				kinds[next] = rvCInt
				next++
			}
			i++
		}
		if i >= len(format) || format[i] == '%' {
			continue
		}
		switch format[i] {
		case 'f', 'F', 'e', 'E', 'g', 'G', 'a', 'A':
			kinds[next] = rvCDouble
		case 's':
			kinds[next] = rvCString
		case 'p':
			kinds[next] = rvCPointer
		default:
			kinds[next] = rvCInt
		}
		next++
	}
	return kinds
}

func rvCTypeKind(t string) rvCKind {
	t = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "const "))
	switch {
	case t == "void":
		return rvCVoid
	case strings.HasSuffix(t, "*"):
		if strings.HasPrefix(t, "char") {
			return rvCString
		}
		return rvCPointer
	case t == "double" || t == "GLdouble":
		return rvCDouble
	case t == "float" || t == "GLfloat" || t == "GLclampf":
		return rvCFloat
	case t == "unsigned int" || t == "uint32_t" || t == "Uint32" || t == "unsigned":
		return rvCUnsigned32
	}
	return rvCInt
}

// argKind classifies an argument without a declared C type.
func (g *rvGen) argKind(e Expression) rvCKind {
	switch e := e.(type) {
	case *StringExpr, *FStringExpr:
		return rvCString
	case *CastExpr:
		switch e.Type {
		case "cstr", "cstring":
			return rvCString
		case "float64", "double":
			return rvCDouble
		case "float32", "float":
			return rvCFloat
		case "ptr", "pointer", "cptr":
			return rvCPointer
		}
	}
	return rvCInt
}

// cCall marshals args per lp64d, calls sym and converts its result into fa0.
func (g *rvGen) cCall(sym string, sig *CFunctionSignature, args []Expression) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	type arg struct {
		kind     rvCKind
		slot     int32
		variadic bool
	}
	list := make([]arg, len(args))
	formatKinds := rvFormatKinds(sym, sig, args)
	for i, a := range args {
		k := g.argKind(a)
		if sig != nil && i < len(sig.Params) {
			if pk := rvCTypeKind(sig.Params[i].Type); pk != rvCVoid {
				k = pk
			}
		} else if fk, ok := formatKinds[i]; ok {
			k = fk
		} else if sig != nil && k == rvCInt {
			k = rvCVariadicNumber
		}
		list[i] = arg{kind: k, slot: g.tmp(), variadic: sig != nil && i >= len(sig.Params)}
		if err := g.cArgument(a, k, list[i].slot); err != nil {
			return err
		}
	}
	ints, floats := 0, 0
	for _, a := range list {
		fp := (a.kind == rvCDouble || a.kind == rvCFloat) && !a.variadic
		switch {
		case fp && floats < 8:
			r := uint32(rvFA0 + floats)
			floats++
			if a.kind == rvCFloat {
				g.memOp(func(b uint32, o int32) { g.w(rvI(o, b, 2, r, 0x07)) }, rvS0, a.slot)
			} else {
				g.fldx(r, rvS0, a.slot)
			}
		case ints < 8:
			g.ldx(uint32(rvA0+ints), rvS0, a.slot)
			ints++
		default:
			return fmt.Errorf("riscv64: %s: more than 8 integer or 8 floating-point arguments are not supported", sym)
		}
	}
	g.flushBeforeC(ints)
	g.callC(sym)
	ret := rvCInt
	if sig != nil {
		ret = rvCTypeKind(sig.ReturnType)
	}
	switch ret {
	case rvCVoid:
		g.number(0)
	case rvCDouble:
	case rvCFloat:
		g.fop(0x21, 0, rvFA0, rvFA0, 0)
	case rvCString:
		null, done := g.newLabel(), g.newLabel()
		g.beqz(rvA0, null)
		g.fmvDX(rvFA0, rvA0)
		g.numCall(valFromUTF8)
		g.j(done)
		g.bind(null)
		g.number(0)
		g.bind(done)
	case rvCPointer:
		g.fcvtDL(rvFA0, rvA0)
	case rvCUnsigned32:
		g.slli(rvA0, rvA0, 32)
		g.srli(rvA0, rvA0, 32)
		g.fromI64()
	default:
		g.fromI64()
	}
	return nil
}

// flushBeforeC empties Tim's output buffer so C output follows it in order,
// keeping the first n integer argument registers intact.
func (g *rvGen) flushBeforeC(n int) {
	skip := g.newLabel()
	g.la(rvT0, g.l["outlen"])
	g.ld(rvT0, rvT0, 0)
	g.beqz(rvT0, skip)
	g.addi(rvSP, rvSP, -64)
	for i := range n {
		g.sd(uint32(rvA0+i), rvSP, int32(8*i))
	}
	g.call(g.l["flush"])
	for i := range n {
		g.ld(uint32(rvA0+i), rvSP, int32(8*i))
	}
	g.addi(rvSP, rvSP, 64)
	g.bind(skip)
}

// cArgument evaluates e converted to C kind k into frame slot slot: integers
// and pointers as int64, doubles as float64, floats as float32 bits.
func (g *rvGen) cArgument(e Expression, k rvCKind, slot int32) error {
	if c, ok := e.(*CastExpr); ok && (c.Type == "cstr" || c.Type == "cstring") {
		e = c.Expr
	}
	if err := g.expr(e); err != nil {
		return err
	}
	switch k {
	case rvCDouble:
		g.toFloat()
		g.fsdx(rvFA0, rvS0, slot)
	case rvCFloat:
		g.toFloat()
		g.fop(0x20, 7, rvFT0, rvFA0, 1)
		g.memOp(func(b uint32, o int32) { g.w(rvS(o, rvFT0, b, 2, 0x27)) }, rvS0, slot)
	case rvCString, rvCPointer:
		g.cPointer()
		g.sdx(rvA0, rvS0, slot)
	case rvCVariadicNumber:
		integral, done := g.newLabel(), g.newLabel()
		g.feq(rvT0, rvFA0, rvFA0)
		g.beqz(rvT0, integral)
		g.fcvtLD(rvT1, rvFA0, 1)
		g.fcvtDL(rvFT0, rvT1)
		g.feq(rvT0, rvFT0, rvFA0)
		g.bnez(rvT0, integral)
		g.fsdx(rvFA0, rvS0, slot)
		g.j(done)
		g.bind(integral)
		g.toI64()
		g.sdx(rvA0, rvS0, slot)
		g.bind(done)
	default:
		g.toI64()
		g.sdx(rvA0, rvS0, slot)
	}
	return nil
}

// cPointer turns fa0 into a C pointer in a0: numbers are addresses, strings
// become NUL-terminated UTF-8 when str is set, other containers are NULL.
// cPointer converts fa0 to a C pointer argument in a0: a string is its
// NUL-terminated UTF-8 bytes, another container its address and a number an
// address.
func (g *rvGen) cPointer() {
	number, done := g.newLabel(), g.newLabel()
	g.fmvXD(rvT0, rvFA0)
	g.boxTest(rvT0, rvT1, number)
	g.mv(rvA0, rvT0)
	g.ld(rvT1, rvT0, 0)
	g.li(rvT2, int64(math.Float64bits(tagString)))
	g.br(1, rvT1, rvT2, done)
	g.numCall(valUTF8)
	g.fmvXD(rvA0, rvFA0)
	g.addi(rvA0, rvA0, 8)
	g.j(done)
	g.bind(number)
	g.toI64()
	g.bind(done)
}

// cConstant resolves ns.NAME against a C header's constants.
func (g *rvGen) cConstant(e *NamespacedIdentExpr) (int64, bool) {
	if g.ffi == nil {
		return 0, false
	}
	if consts := g.ffi.constants[e.Namespace]; consts != nil {
		v, ok := consts.Constants[e.Name]
		return v, ok
	}
	return 0, false
}

// neededLibs lists the shared libraries the program links against.
func (g *rvGen) neededLibs() []string {
	libs := []string{"libc.so.6", "libm.so.6"}
	seen := map[string]bool{"libc.so.6": true, "libm.so.6": true}
	for _, lib := range g.ffi.imports {
		if name := rvSoname(lib); !seen[name] {
			seen[name] = true
			libs = append(libs, name)
		}
	}
	return libs
}

// rvSoname finds the runtime name of a library: an explicit .so name, or
// the SONAME of the library ldconfig knows by that name.
func rvSoname(lib string) string {
	if strings.Contains(lib, ".so") {
		return filepath.Base(lib)
	}
	name := lib
	if out, err := exec.Command("pkg-config", "--libs-only-l", lib).Output(); err == nil {
		for f := range strings.FieldsSeq(string(out)) {
			if l, ok := strings.CutPrefix(f, "-l"); ok {
				name = l
				break
			}
		}
	}
	if !strings.HasPrefix(name, "lib") {
		name = "lib" + name
	}
	if out, err := exec.Command("ldconfig", "-p").Output(); err == nil {
		for line := range strings.SplitSeq(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || !strings.HasPrefix(fields[0], name+".so") {
				continue
			}
			path := fields[len(fields)-1]
			if f, err := elf.Open(path); err == nil {
				sonames, _ := f.DynString(elf.DT_SONAME)
				f.Close()
				if len(sonames) > 0 {
					return sonames[0]
				}
			}
			return fields[0]
		}
	}
	return name + ".so"
}
