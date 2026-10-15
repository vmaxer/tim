package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
)

// The riscv64 backend emits a static, libc-free ELF with two segments: code
// and read-only data (R+X), then zero-initialized globals, heap state and the
// output buffer (R+W, page aligned so stores never share a page with code).
// Values are NaN-boxed doubles computed in fa0. Variables live in callee-saved
// FP registers (riscv64_regalloc.go), spilling to s0-relative frame slots like
// temporaries; variables shared with functions are globals in the s1-relative
// data area. Containers,
// printing and exact numbers are handled by the shared C runtime (numrt).

const (
	rvRA, rvSP             = 1, 2
	rvT0, rvT1, rvT2       = 5, 6, 7
	rvS0, rvS1             = 8, 9
	rvA0, rvA1, rvA2, rvA3 = 10, 11, 12, 13
	rvA4, rvA5, rvA7       = 14, 15, 17
	rvS2, rvS3, rvS4       = 18, 19, 20
	rvT3, rvT4, rvT5, rvT6 = 28, 29, 30, 31
	rvFT0, rvFT1, rvFT2    = 0, 1, 2
	rvFS0                  = 8
	rvFA0, rvFA1           = 10, 11
	rvBase                 = 0x10000
	rvPage                 = 0x1000
	rvMaxArgs              = 8
	rvPrologueWords        = 7
	rvTagFunc              = 4.0
)

type rvLabel struct{ pos int }

type rvFixup struct {
	at     int
	l      *rvLabel
	add    int
	pair   bool
	branch bool
}

var errRVLongBranch = errors.New("riscv64: conditional branch out of range")

type rvCapture struct {
	name string
	val  Expression
}

type rvFunc struct {
	lam      *LambdaExpr
	name     string
	params   []string
	body     Expression
	label    *rvLabel
	captures []rvCapture
	direct   bool
	static   *rvLabel
}

type rvScope struct {
	fn        *rvFunc
	vars      map[string]int32
	env       map[string]int32
	envSlot   int32
	numeric   map[string]bool
	nvars     int
	ntmp      int
	maxTmp    int
	prologue  int
	ret       *rvLabel
	bodyStart *rvLabel
	loops     []rvLoop
	tailExpr  Expression
	structs   map[string]string
	defers    []rvDefer
	regs      map[string]uint32
	saves     []rvSave
	intPool   []uint32
}

type rvSave struct {
	reg  uint32
	slot int32
	fp   bool
}

type rvDefer struct {
	call Expression
	flag int32
}

type rvLoop struct{ cont, brk *rvLabel }

type rvData struct {
	l     *rvLabel
	bytes []byte
}

type rvGen struct {
	unsafeVals    map[*CastExpr]int32
	unsafeDepth   int32
	code          []byte
	fixups        []rvFixup
	rodata        []rvData
	statics       []*rvFunc
	top           []Statement
	globals       map[string]int32
	globalNum     map[string]bool
	funcs         map[string]*rvFunc
	pending       []*rvFunc
	sc            *rvScope
	longBranches  bool
	mutable       map[string]bool
	ffi           *rvFFI
	dynamic       bool
	imports       []rvImport
	hdr           int
	strs          map[string]*rvLabel
	l             map[string]*rvLabel
	nlambda       int
	structs       map[string]*CStructDecl
	mainLocals    map[string]bool
	varStructs    map[string]string
	containerVars map[string]bool
	typing        map[string]bool
}

var rvDataArea = []struct {
	name string
	size int
}{{"heap", 16}, {"outlen", 8}, {"outfd", 8}, {"outtty", 8}, {"outbuf", 4096}}

// compileRV64 emits forward branches as single instructions, falling back to
// branch-over-jump pairs everywhere when some branch cannot reach its target.
func compileRV64(program *Program, ffi *rvFFI) ([]byte, error) {
	img, err := compileRV64Branches(program, ffi, false)
	if errors.Is(err, errRVLongBranch) {
		return compileRV64Branches(program, ffi, true)
	}
	return img, err
}

func compileRV64Branches(program *Program, ffi *rvFFI, longBranches bool) ([]byte, error) {
	g := &rvGen{
		ffi:           ffi,
		dynamic:       ffi != nil && rvUsesFFI(program, ffi),
		longBranches:  longBranches,
		globals:       map[string]int32{},
		globalNum:     map[string]bool{},
		funcs:         map[string]*rvFunc{},
		strs:          map[string]*rvLabel{},
		l:             map[string]*rvLabel{},
		structs:       program.CStructs,
		varStructs:    map[string]string{},
		mainLocals:    map[string]bool{},
		containerVars: map[string]bool{},
		typing:        map[string]bool{},
	}
	for _, name := range []string{"num", "alloc", "putc", "flush", "print_raw", "print_val", "print_i64", "print_hex", "print_fixed", "blob", "globals", "main", "end"} {
		g.l[name] = g.newLabel()
	}
	for _, d := range rvDataArea {
		g.l[d.name] = g.newLabel()
	}

	updated := map[string]bool{}
	for _, s := range program.Statements {
		rvWalkAssigns(s, func(name string, update bool) {
			if update {
				updated[name] = true
			}
		})
	}
	var top []Statement
	for _, s := range program.Statements {
		if a, ok := s.(*AssignStmt); ok && !a.Mutable && !a.IsUpdate && !updated[a.Name] {
			if lam, ok := a.Value.(*LambdaExpr); ok && lam.VariadicParam == "" {
				if f := g.funcs[a.Name]; f != nil {
					f.lam, f.params, f.body = lam, lam.Params, lam.Body
					continue
				}
				f := &rvFunc{lam: lam, name: a.Name, params: lam.Params, body: lam.Body, label: g.newLabel(), direct: true}
				g.funcs[a.Name] = f
				g.pending = append(g.pending, f)
				continue
			}
		}
		top = append(top, s)
	}
	shared := map[string]bool{}
	astVisit(&program.Statements, true, func(n any) {
		if lam, ok := n.(*LambdaExpr); ok {
			astVisit(lam.Body, true, func(m any) { rvAddNames(m, shared) })
		}
	})
	for _, s := range top {
		rvCollectNames(s, func(name string, _ bool) {
			if g.funcs[name] != nil {
				return
			}
			if !shared[name] {
				g.mainLocals[name] = true
				return
			}
			if _, ok := g.globals[name]; !ok {
				g.globals[name] = int32(8 * len(g.globals))
			}
		})
	}
	g.top = top
	g.mutable = map[string]bool{}
	astVisit(&program.Statements, true, func(n any) {
		switch a := n.(type) {
		case *AssignStmt:
			if a.Mutable && !a.IsUpdate {
				g.mutable[a.Name] = true
			}
		case *MultipleAssignStmt:
			if a.Mutable {
				for _, name := range a.Names {
					g.mutable[name] = true
				}
			}
		}
	})
	g.globalNum = g.inferNumeric(program.Statements, g.globals)
	g.inferStructVars(program.Statements)

	g.emitStart()
	g.emitHelpers()

	g.bind(g.l["main"])
	if err := g.compileBody(nil, func() error {
		if err := g.stmts(top); err != nil {
			return err
		}
		if f := g.funcs["main"]; f != nil && len(f.params) == 0 && !(&TimCompiler{}).detectMainCallInTopLevel(top) {
			return g.callExpr(&CallExpr{Function: "main"})
		}
		if g.funcs["main"] == nil {
			g.number(0)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for len(g.pending) > 0 {
		f := g.pending[0]
		g.pending = g.pending[1:]
		g.bind(f.label)
		if err := g.compileBody(f, func() error { return g.inTail(f.body, true) }); err != nil {
			return nil, fmt.Errorf("riscv64: in %s: %w", f.name, err)
		}
	}

	for len(g.code)%64 != 0 {
		g.w(0x00000013)
	}
	g.bind(g.l["blob"])
	g.code = append(g.code, numRuntimeRISCV64...)
	for _, f := range g.statics {
		g.align(8)
		g.bind(f.static)
		g.code = binary.LittleEndian.AppendUint64(g.code, math.Float64bits(rvTagFunc))
		g.code = binary.LittleEndian.AppendUint64(g.code, 0)
		g.code = binary.LittleEndian.AppendUint64(g.code, 0)
	}
	for _, d := range g.rodata {
		g.align(8)
		g.bind(d.l)
		g.code = append(g.code, d.bytes...)
	}
	g.hdr = 64 + 2*56
	var dyn *rvDynamic
	if g.dynamic {
		g.hdr = 64 + 4*56
		dyn = g.emitDynamicTables()
	}
	for _, f := range g.statics {
		binary.LittleEndian.PutUint64(g.code[f.static.pos+8:], uint64(rvBase+g.hdr+f.label.pos))
	}
	g.align(8)
	g.bind(g.l["end"])

	data := (g.hdr+len(g.code)+rvPage-1)&^(rvPage-1) - g.hdr
	rw := data
	if dyn != nil {
		data += 16 * len(dyn.entries)
	}
	g.l["globals"].pos = data
	data += 8 * len(g.globals)
	for _, d := range rvDataArea {
		data = (data + 7) &^ 7
		g.l[d.name].pos = data
		data += d.size
	}
	for _, im := range g.imports {
		im.slot.pos = data
		data += 8
	}

	for _, f := range g.fixups {
		off := f.l.pos + f.add - f.at
		if f.branch {
			if off < -4096 || off >= 4096 {
				return nil, errRVLongBranch
			}
			ins := g.get(f.at)
			g.put(f.at, rvB(int32(off), ins>>20&31, ins>>15&31, ins>>12&7))
			continue
		}
		if f.pair {
			hi := (off + 0x800) >> 12
			lo := off - hi<<12
			g.put(f.at, g.get(f.at)|uint32(hi)&0xfffff<<12)
			g.put(f.at+4, g.get(f.at+4)|uint32(lo)&0xfff<<20)
			continue
		}
		if off < -(1<<20) || off >= 1<<20 {
			return nil, fmt.Errorf("riscv64: jump out of range")
		}
		g.put(f.at, rvJ(int32(off), g.get(f.at)>>7&31))
	}
	return g.elf(rw, data, dyn), nil
}

// elf lays the image out as [ELF header][code, rodata, dynamic tables] (R+X)
// and, page aligned at rw, [.dynamic][zero-initialized data] (R+W) ending at
// end; positions are relative to the end of the header.
func (g *rvGen) elf(rw, end int, dyn *rvDynamic) []byte {
	le := binary.LittleEndian
	addr := func(pos int) uint64 { return uint64(rvBase + g.hdr + pos) }
	off := func(pos int) uint64 { return uint64(g.hdr + pos) }
	var dynBytes []byte
	if dyn != nil {
		for _, e := range dyn.entries {
			dynBytes = le.AppendUint64(dynBytes, uint64(e[0]))
			dynBytes = le.AppendUint64(dynBytes, e[1])
		}
		for i, im := range g.imports {
			at := dyn.rela + 24*i
			le.PutUint64(g.code[at:], addr(im.slot.pos))
			le.PutUint64(g.code[at+8:], uint64(i+1)<<32|2)
		}
	}
	type seg struct {
		typ, flags          uint32
		off, vaddr, fsz, ms uint64
		align               uint64
	}
	codeEnd := off(g.l["end"].pos)
	var segs []seg
	if dyn != nil {
		segs = append(segs, seg{3, 4, off(dyn.interp), addr(dyn.interp), uint64(len(rvInterp) + 1), uint64(len(rvInterp) + 1), 1})
	}
	segs = append(segs,
		seg{1, 5, 0, rvBase, codeEnd, codeEnd, rvPage},
		seg{1, 6, off(rw), addr(rw), uint64(len(dynBytes)), uint64(end - rw), rvPage})
	if dyn != nil {
		segs = append(segs, seg{2, 6, off(rw), addr(rw), uint64(len(dynBytes)), uint64(len(dynBytes)), 8})
	}
	b := []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	b = le.AppendUint16(b, 2)
	b = le.AppendUint16(b, 243)
	b = le.AppendUint32(b, 1)
	b = le.AppendUint64(b, rvBase+uint64(g.hdr))
	b = le.AppendUint64(b, 64)
	b = le.AppendUint64(b, 0)
	b = le.AppendUint32(b, 0x5)
	b = le.AppendUint16(b, 64)
	b = le.AppendUint16(b, 56)
	b = le.AppendUint16(b, uint16(len(segs)))
	b = le.AppendUint16(b, 64)
	b = le.AppendUint16(b, 0)
	b = le.AppendUint16(b, 0)
	for _, sg := range segs {
		b = le.AppendUint32(b, sg.typ)
		b = le.AppendUint32(b, sg.flags)
		b = le.AppendUint64(b, sg.off)
		b = le.AppendUint64(b, sg.vaddr)
		b = le.AppendUint64(b, sg.vaddr)
		b = le.AppendUint64(b, sg.fsz)
		b = le.AppendUint64(b, sg.ms)
		b = le.AppendUint64(b, sg.align)
	}
	b = append(b, g.code...)
	if dyn != nil {
		b = append(b, make([]byte, int(off(rw))-len(b))...)
		b = append(b, dynBytes...)
	}
	return b
}

const rvInterp = "/lib/ld-linux-riscv64-lp64d.so.1"

type rvDynamic struct {
	interp, rela int
	entries      [][2]uint64
}

// emitDynamicTables appends the interpreter path, dynamic string and symbol
// tables, a symbol hash table and the GOT relocations (filled in by elf once
// the GOT is placed) to the read-only image, and returns .dynamic's entries.
func (g *rvGen) emitDynamicTables() *rvDynamic {
	le := binary.LittleEndian
	d := &rvDynamic{}
	put := func(b []byte) int {
		g.align(8)
		at := len(g.code)
		g.code = append(g.code, b...)
		return at
	}
	d.interp = put(append([]byte(rvInterp), 0))
	strs := []byte{0}
	str := func(s string) uint64 {
		at := len(strs)
		strs = append(append(strs, s...), 0)
		return uint64(at)
	}
	var needed []uint64
	for _, lib := range g.neededLibs() {
		needed = append(needed, str(lib))
	}
	syms := make([]byte, 24)
	for _, im := range g.imports {
		var sym [24]byte
		le.PutUint32(sym[0:], uint32(str(im.sym)))
		sym[4] = 1<<4 | 2
		syms = append(syms, sym[:]...)
	}
	nsyms := len(g.imports) + 1
	hash := le.AppendUint32(nil, 1)
	hash = le.AppendUint32(hash, uint32(nsyms))
	hash = le.AppendUint32(hash, 0)
	hash = append(hash, make([]byte, 4*nsyms)...)
	strAt := put(strs)
	symAt := put(syms)
	hashAt := put(hash)
	d.rela = put(make([]byte, 24*len(g.imports)))
	addr := func(pos int) uint64 { return uint64(rvBase + g.hdr + pos) }
	for _, n := range needed {
		d.entries = append(d.entries, [2]uint64{1, n})
	}
	d.entries = append(d.entries,
		[2]uint64{4, addr(hashAt)},
		[2]uint64{5, addr(strAt)},
		[2]uint64{6, addr(symAt)},
		[2]uint64{10, uint64(len(strs))},
		[2]uint64{11, 24},
		[2]uint64{7, addr(d.rela)},
		[2]uint64{8, uint64(24 * len(g.imports))},
		[2]uint64{9, 24},
		[2]uint64{30, 8},
		[2]uint64{0x6ffffffb, 1},
		[2]uint64{21, 0},
		[2]uint64{0, 0},
	)
	return d
}

// rvWalkAssigns reports every assignment target in a statement tree,
// lambdas included.
func rvWalkAssigns(node any, add func(name string, update bool)) {
	rvVisit(node, true, func(n any) {
		switch s := n.(type) {
		case *AssignStmt:
			add(s.Name, s.IsUpdate)
		case *MultipleAssignStmt:
			for _, name := range s.Names {
				add(name, s.IsUpdate)
			}
		case *MapUpdateStmt:
			add(s.MapName, true)
		}
	})
}

// rvCollectNames reports every variable a statement list binds (update is
// set for <-), without descending into lambdas.
func rvCollectNames(node any, add func(name string, update bool)) {
	rvVisit(node, false, func(n any) {
		switch s := n.(type) {
		case *AssignStmt:
			add(s.Name, s.IsUpdate)
		case *MultipleAssignStmt:
			for _, name := range s.Names {
				add(name, s.IsUpdate)
			}
		case *LoopStmt:
			add(s.Iterator, false)
			if s.KeyVar != "" {
				add(s.KeyVar, false)
			}
		case *ReceiveLoopStmt:
			for _, name := range []string{s.MessageVar, s.SenderVar} {
				if name != "" {
					add(name, false)
				}
			}
		case *PostfixExpr:
			if id, ok := s.Operand.(*IdentExpr); ok {
				add(id.Name, true)
			}
		}
	})
}

// rvVisit calls f for every node of an AST subtree in source order,
// descending into lambda bodies only when lambdas is set.
func rvVisit(node any, lambdas bool, f func(any)) { astVisit(node, lambdas, f) }

// Encoding.

func rvR(f7, rs2, rs1, f3, rd, op uint32) uint32 {
	return f7<<25 | rs2<<20 | rs1<<15 | f3<<12 | rd<<7 | op
}

func rvI(imm int32, rs1, f3, rd, op uint32) uint32 {
	return uint32(imm)&0xfff<<20 | rs1<<15 | f3<<12 | rd<<7 | op
}

func rvS(imm int32, rs2, rs1, f3, op uint32) uint32 {
	u := uint32(imm)
	return u>>5&0x7f<<25 | rs2<<20 | rs1<<15 | f3<<12 | u&0x1f<<7 | op
}

func rvB(imm int32, rs2, rs1, f3 uint32) uint32 {
	u := uint32(imm)
	return u>>12&1<<31 | u>>5&0x3f<<25 | rs2<<20 | rs1<<15 | f3<<12 | u>>1&0xf<<8 | u>>11&1<<7 | 0x63
}

func rvJ(imm int32, rd uint32) uint32 {
	u := uint32(imm)
	return u>>20&1<<31 | u>>1&0x3ff<<21 | u>>11&1<<20 | u>>12&0xff<<12 | rd<<7 | 0x6f
}

func rvFits12(v int32) bool { return v >= -2048 && v < 2048 }

func (g *rvGen) w(x uint32)                      { g.code = binary.LittleEndian.AppendUint32(g.code, x) }
func (g *rvGen) get(at int) uint32               { return binary.LittleEndian.Uint32(g.code[at:]) }
func (g *rvGen) put(at int, x uint32)            { binary.LittleEndian.PutUint32(g.code[at:], x) }
func (g *rvGen) newLabel() *rvLabel              { return &rvLabel{pos: -1} }
func (g *rvGen) bind(l *rvLabel)                 { l.pos = len(g.code) }
func (g *rvGen) align(n int)                     { g.code = append(g.code, make([]byte, (n-len(g.code)%n)%n)...) }
func (g *rvGen) addi(rd, rs uint32, i int32)     { g.w(rvI(i, rs, 0, rd, 0x13)) }
func (g *rvGen) andi(rd, rs uint32, i int32)     { g.w(rvI(i, rs, 7, rd, 0x13)) }
func (g *rvGen) xori(rd, rs uint32, i int32)     { g.w(rvI(i, rs, 4, rd, 0x13)) }
func (g *rvGen) sltiu(rd, rs uint32, i int32)    { g.w(rvI(i, rs, 3, rd, 0x13)) }
func (g *rvGen) slli(rd, rs uint32, sh int32)    { g.w(rvI(sh, rs, 1, rd, 0x13)) }
func (g *rvGen) srli(rd, rs uint32, sh int32)    { g.w(rvI(sh, rs, 5, rd, 0x13)) }
func (g *rvGen) mv(rd, rs uint32)                { g.addi(rd, rs, 0) }
func (g *rvGen) op(f7, f3, rd, rs1, rs2 uint32)  { g.w(rvR(f7, rs2, rs1, f3, rd, 0x33)) }
func (g *rvGen) add(rd, rs1, rs2 uint32)         { g.op(0, 0, rd, rs1, rs2) }
func (g *rvGen) ld(rd, rs uint32, off int32)     { g.w(rvI(off, rs, 3, rd, 0x03)) }
func (g *rvGen) lbu(rd, rs uint32, off int32)    { g.w(rvI(off, rs, 4, rd, 0x03)) }
func (g *rvGen) sd(rs2, rs1 uint32, off int32)   { g.w(rvS(off, rs2, rs1, 3, 0x23)) }
func (g *rvGen) sb(rs2, rs1 uint32, off int32)   { g.w(rvS(off, rs2, rs1, 0, 0x23)) }
func (g *rvGen) fld(fd, rs uint32, off int32)    { g.w(rvI(off, rs, 3, fd, 0x07)) }
func (g *rvGen) fsd(fs, rs uint32, off int32)    { g.w(rvS(off, fs, rs, 3, 0x27)) }
func (g *rvGen) fop(f7, rm, rd, rs1, rs2 uint32) { g.w(rvR(f7, rs2, rs1, rm, rd, 0x53)) }
func (g *rvGen) fmvXD(rd, fs uint32)             { g.fop(0x71, 0, rd, fs, 0) }
func (g *rvGen) fmvDX(fd, rs uint32)             { g.fop(0x79, 0, fd, rs, 0) }
func (g *rvGen) fmvD(fd, fs uint32)              { g.fop(0x11, 0, fd, fs, fs) }
func (g *rvGen) fcvtLD(rd, fs, rm uint32)        { g.fop(0x61, rm, rd, fs, 2) }
func (g *rvGen) fcvtDL(fd, rs uint32)            { g.fop(0x69, 7, fd, rs, 2) }
func (g *rvGen) feq(rd, a, b uint32)             { g.fop(0x51, 2, rd, a, b) }
func (g *rvGen) ecall()                          { g.w(0x73) }
func (g *rvGen) jalr(rd, rs uint32, off int32)   { g.w(rvI(off, rs, 0, rd, 0x67)) }
func (g *rvGen) ret()                            { g.jalr(0, rvRA, 0) }

// Frame and data accesses whose offset may exceed 12 bits go through t6.
func (g *rvGen) memOp(emit func(base uint32, off int32), base uint32, off int32) {
	if rvFits12(off) {
		emit(base, off)
		return
	}
	g.li(rvT6, int64(off))
	g.add(rvT6, rvT6, base)
	emit(rvT6, 0)
}

func (g *rvGen) ldx(rd, base uint32, off int32) {
	g.memOp(func(b uint32, o int32) { g.ld(rd, b, o) }, base, off)
}

func (g *rvGen) sdx(rs, base uint32, off int32) {
	g.memOp(func(b uint32, o int32) { g.sd(rs, b, o) }, base, off)
}

func (g *rvGen) fldx(fd, base uint32, off int32) {
	g.memOp(func(b uint32, o int32) { g.fld(fd, b, o) }, base, off)
}

func (g *rvGen) fsdx(fs, base uint32, off int32) {
	g.memOp(func(b uint32, o int32) { g.fsd(fs, b, o) }, base, off)
}

func (g *rvGen) li(rd uint32, v int64) {
	if v >= -2048 && v < 2048 {
		g.addi(rd, 0, int32(v))
		return
	}
	if v == int64(int32(v)) {
		hi := (v + 0x800) >> 12
		g.w(uint32(hi)&0xfffff<<12 | rd<<7 | 0x37)
		if lo := v - hi<<12; lo != 0 {
			g.w(rvI(int32(lo), rd, 0, rd, 0x1b))
		}
		return
	}
	lo := v << 52 >> 52
	hi, sh := (v-lo)>>12, int32(12)
	for hi&1 == 0 {
		hi >>= 1
		sh++
	}
	g.li(rd, hi)
	g.slli(rd, rd, sh)
	if lo != 0 {
		g.addi(rd, rd, int32(lo))
	}
}

func (g *rvGen) jal(rd uint32, l *rvLabel, add int) {
	g.fixups = append(g.fixups, rvFixup{at: len(g.code), l: l, add: add})
	g.w(rvJ(0, rd))
}

func (g *rvGen) j(l *rvLabel)    { g.jal(0, l, 0) }
func (g *rvGen) call(l *rvLabel) { g.jal(rvRA, l, 0) }

func (g *rvGen) la(rd uint32, l *rvLabel) {
	g.fixups = append(g.fixups, rvFixup{at: len(g.code), l: l, pair: true})
	g.w(0x17 | rd<<7)
	g.addi(rd, rd, 0)
}

// br branches to l when rs1 <f3> rs2 (f3: 0 eq, 1 ne, 4 lt, 5 ge, 6 ltu, 7 geu).
// Bound labels within reach and forward labels get a single branch (forward
// ones are checked at link time); in long-branch mode forward branches are an
// inverted branch around a jal.
func (g *rvGen) br(f3, rs1, rs2 uint32, l *rvLabel) {
	if l.pos >= 0 {
		if off := l.pos - len(g.code); off >= -4096 {
			g.w(rvB(int32(off), rs2, rs1, f3))
			return
		}
	}
	if !g.longBranches {
		g.fixups = append(g.fixups, rvFixup{at: len(g.code), l: l, branch: true})
		g.w(rvB(0, rs2, rs1, f3))
		return
	}
	g.w(rvB(8, rs2, rs1, f3^1))
	g.j(l)
}

func (g *rvGen) beqz(rs uint32, l *rvLabel) { g.br(0, rs, 0, l) }
func (g *rvGen) bnez(rs uint32, l *rvLabel) { g.br(1, rs, 0, l) }

func (g *rvGen) constData(b []byte) *rvLabel {
	l := g.newLabel()
	g.rodata = append(g.rodata, rvData{l, b})
	return l
}

// Functions and frames.

func (g *rvGen) compileBody(f *rvFunc, body func() error) error {
	sc := &rvScope{fn: f, vars: map[string]int32{}, env: map[string]int32{}, structs: map[string]string{}, ret: g.newLabel(), bodyStart: g.newLabel()}
	saved := g.sc
	g.sc = sc
	defer func() { g.sc = saved }()
	sc.prologue = len(g.code)
	for range rvPrologueWords {
		g.w(0)
	}
	if f != nil {
		if len(f.params) > rvMaxArgs {
			return fmt.Errorf("more than %d parameters", rvMaxArgs)
		}
		for _, p := range f.params {
			sc.newVar(p)
		}
		if len(f.captures) > 0 {
			sc.envSlot = sc.newVar("\x00env")
			g.sdx(rvA0, rvS0, sc.envSlot)
			for i, c := range f.captures {
				if _, isParam := sc.vars[c.name]; !isParam {
					sc.env[c.name] = int32(24 + 8*i)
				}
			}
		}
		rvCollectNames(f.body, func(name string, update bool) {
			if _, ok := sc.vars[name]; ok {
				return
			}
			if _, ok := sc.env[name]; ok && update {
				return
			}
			if _, global := g.globals[name]; update && global {
				return
			}
			sc.newVar(name)
		})
		sc.tailExpr = f.body
		sc.numeric = g.inferNumeric([]Statement{&ExpressionStmt{Expr: f.body}}, sc.vars)
		for _, p := range f.params {
			delete(sc.numeric, p)
		}
		if f.lam != nil {
			for p, t := range f.lam.ParamCStructTypes {
				if g.cstruct(t) != nil {
					sc.structs[p] = t
				}
			}
		}
	}
	var root any = &g.top
	if f != nil {
		root = f.body
	} else {
		for _, s := range g.top {
			rvCollectNames(s, func(name string, _ bool) {
				if _, ok := sc.vars[name]; !ok && g.mainLocals[name] {
					sc.newVar(name)
				}
			})
		}
		sc.numeric = g.inferNumeric(g.top, sc.vars)
	}
	rvVisit(root, false, func(n any) {
		if d, ok := n.(*DeferStmt); ok {
			sc.defers = append(sc.defers, rvDefer{d.Call, sc.newVar(fmt.Sprintf("\x00defer%d", len(sc.defers)))})
		}
	})
	g.allocRegs(root)
	for _, sv := range sc.saves {
		if sv.fp {
			g.fsdx(sv.reg, rvS0, sv.slot)
		} else {
			g.sdx(sv.reg, rvS0, sv.slot)
		}
	}
	if f != nil {
		for i, p := range f.params {
			if err := g.storeVar(uint32(rvFA0+i), p); err != nil {
				return err
			}
		}
	}
	for _, d := range sc.defers {
		g.sdx(0, rvS0, d.flag)
	}
	g.bind(sc.bodyStart)
	if err := body(); err != nil {
		return err
	}
	g.bind(sc.ret)
	if len(sc.defers) > 0 {
		saved := g.tmp()
		g.fsdx(rvFA0, rvS0, saved)
		for _, v := range slices.Backward(sc.defers) {
			skip := g.newLabel()
			g.ldx(rvT0, rvS0, v.flag)
			g.beqz(rvT0, skip)
			if err := g.expr(v.call); err != nil {
				return err
			}
			g.bind(skip)
		}
		g.fldx(rvFA0, rvS0, saved)
	}
	for _, sv := range sc.saves {
		if sv.fp {
			g.fldx(sv.reg, rvS0, sv.slot)
		} else {
			g.ldx(sv.reg, rvS0, sv.slot)
		}
	}
	g.ld(rvRA, rvS0, -8)
	g.mv(rvT0, rvS0)
	g.ld(rvS0, rvS0, -16)
	g.mv(rvSP, rvT0)
	g.ret()

	frame := int32(16+8*(sc.nvars+sc.maxTmp)+15) &^ 15
	words := []uint32{
		rvI(-frame, rvSP, 0, rvSP, 0x13),
		rvS(frame-8, rvRA, rvSP, 3, 0x23),
		rvS(frame-16, rvS0, rvSP, 3, 0x23),
		rvI(frame, rvSP, 0, rvS0, 0x13),
	}
	if !rvFits12(frame) {
		hi := (frame + 0x800) >> 12
		words = []uint32{
			uint32(hi)&0xfffff<<12 | rvT6<<7 | 0x37,
			rvI(frame-hi<<12, rvT6, 0, rvT6, 0x1b),
			rvR(0x20, rvT6, rvSP, 0, rvSP, 0x33),
			rvR(0, rvT6, rvSP, 0, rvT6, 0x33),
			rvS(-8, rvRA, rvT6, 3, 0x23),
			rvS(-16, rvS0, rvT6, 3, 0x23),
			rvI(0, rvT6, 0, rvS0, 0x13),
		}
	}
	for i := range rvPrologueWords {
		ins := uint32(0x13)
		if i < len(words) {
			ins = words[i]
		}
		g.put(sc.prologue+4*i, ins)
	}
	return nil
}

func (sc *rvScope) newVar(name string) int32 {
	sc.nvars++
	off := int32(-16 - 8*sc.nvars)
	sc.vars[name] = off
	return off
}

// tmp reserves a temporary slot above all locals (which are collected before
// the body is compiled); release it by restoring sc.ntmp.
func (g *rvGen) tmp() int32 {
	sc := g.sc
	sc.ntmp++
	sc.maxTmp = max(sc.maxTmp, sc.ntmp)
	return int32(-16 - 8*(sc.nvars+sc.ntmp))
}

func (g *rvGen) tmps(n int) int32 {
	var last int32
	for range n {
		last = g.tmp()
	}
	return last
}

// Variables.

func (g *rvGen) isVar(name string) bool {
	if _, ok := g.sc.vars[name]; ok {
		return true
	}
	if _, ok := g.sc.env[name]; ok {
		return true
	}
	_, ok := g.globals[name]
	return ok
}

func (g *rvGen) loadVar(fd uint32, name string) error {
	if r, ok := g.sc.regs[name]; ok {
		if r != fd {
			g.fmvD(fd, r)
		}
		return nil
	}
	if off, ok := g.sc.vars[name]; ok {
		g.fldx(fd, rvS0, off)
		return nil
	}
	if off, ok := g.sc.env[name]; ok {
		g.ldx(rvT0, rvS0, g.sc.envSlot)
		g.fld(fd, rvT0, off)
		return nil
	}
	if off, ok := g.globals[name]; ok {
		g.fldx(fd, rvS1, off)
		return nil
	}
	if f := g.funcs[name]; f != nil {
		g.la(rvT0, g.staticClosure(f))
		g.fmvDX(fd, rvT0)
		return nil
	}
	return fmt.Errorf("riscv64: undefined variable %s", name)
}

func (g *rvGen) storeVar(fs uint32, name string) error {
	if r, ok := g.sc.regs[name]; ok {
		if r != fs {
			g.fmvD(r, fs)
		}
		return nil
	}
	if off, ok := g.sc.vars[name]; ok {
		g.fsdx(fs, rvS0, off)
		return nil
	}
	if off, ok := g.sc.env[name]; ok {
		g.ldx(rvT0, rvS0, g.sc.envSlot)
		g.fsd(fs, rvT0, off)
		return nil
	}
	if off, ok := g.globals[name]; ok {
		g.fsdx(fs, rvS1, off)
		return nil
	}
	return fmt.Errorf("riscv64: undefined variable %s", name)
}

func (g *rvGen) staticClosure(f *rvFunc) *rvLabel {
	if f.static == nil {
		f.static = g.newLabel()
		g.statics = append(g.statics, f)
	}
	return f.static
}

// Static numeric inference: a variable is numeric when every value bound to
// it is. Used only to skip container checks on arithmetic fast paths.

func (g *rvGen) inferNumeric(stmts []Statement, vars map[string]int32) map[string]bool {
	num := map[string]bool{}
	for name := range vars {
		num[name] = true
	}
	for changed := true; changed; {
		changed = false
		kill := func(name string) {
			if num[name] {
				num[name] = false
				changed = true
			}
		}
		for _, s := range stmts {
			rvVisit(s, true, func(n any) {
				switch s := n.(type) {
				case *AssignStmt:
					if !rvIsNum(s.Value, num) {
						kill(s.Name)
					}
				case *MultipleAssignStmt:
					for _, name := range s.Names {
						kill(name)
					}
				case *LoopStmt:
					if _, ok := s.Iterable.(*RangeExpr); !ok {
						kill(s.Iterator)
					}
				case *LambdaExpr:
					for _, p := range s.Params {
						kill(p)
					}
				}
			})
		}
	}
	for name, ok := range num {
		if !ok {
			delete(num, name)
		}
	}
	return num
}

func rvIsNum(e Expression, num map[string]bool) bool {
	switch e := e.(type) {
	case *NumberExpr, *BooleanExpr, *LengthExpr:
		return true
	case *IdentExpr:
		return num[e.Name]
	case *UnaryExpr:
		return true
	case *BinaryExpr:
		switch e.Operator {
		case "+", "*", "or!":
			return rvIsNum(e.Left, num) && rvIsNum(e.Right, num)
		case "::":
			return false
		}
		return true
	case *FMAExpr:
		return true
	case *CallExpr:
		switch e.Function {
		case "sqrt", "floor", "ceil", "round", "abs", "trunc", "float", "pow", "min", "max",
			"sin", "cos", "tan", "asin", "acos", "atan", "atan2", "exp", "log", "log10",
			"popcount", "clz", "ctz", "is_nan":
			return e.Function != "min" && e.Function != "max" || rvIsNum(e.Args[0], num) && rvIsNum(e.Args[len(e.Args)-1], num)
		}
	case *CastExpr:
		switch e.Type {
		case "string", "str", "list", "map", "ptr", "pointer", "cstr", "cstring", "addr":
			return false
		}
		return true
	case *MatchExpr:
		for _, c := range e.Clauses {
			if c.Result != nil && !rvIsNum(c.Result, num) {
				return false
			}
		}
		return e.DefaultExpr == nil || rvIsNum(e.DefaultExpr, num)
	case *BlockExpr:
		if n := len(e.Statements); n > 0 {
			if s, ok := e.Statements[n-1].(*ExpressionStmt); ok {
				return rvIsNum(s.Expr, num)
			}
		}
	}
	return false
}

func (g *rvGen) isNum(e Expression) bool {
	num := map[string]bool{}
	for name := range g.globalNum {
		_, local := g.sc.vars[name]
		_, env := g.sc.env[name]
		if !local && !env {
			num[name] = true
		}
	}
	for name := range g.sc.numeric {
		num[name] = true
	}
	return rvIsNum(e, num)
}

// Statements.

func (g *rvGen) stmts(list []Statement) error {
	for _, s := range list {
		if err := g.stmt(s); err != nil {
			return err
		}
	}
	return nil
}

// stmt compiles a statement, leaving its value (0 when it has none) in fa0.
func (g *rvGen) stmt(s Statement) error {
	switch s.(type) {
	case *LoopStmt, *WhileStmt, *IfStmt, *MapUpdateStmt, *MultipleAssignStmt, *ReceiveLoopStmt:
		defer g.number(0)
	}
	switch s := s.(type) {
	case *ExpressionStmt:
		if p, ok := s.Expr.(*PostfixExpr); ok {
			return g.postfix(p)
		}
		return g.expr(s.Expr)
	case *AssignStmt:
		if f := g.funcs[s.Name]; f != nil && g.sc.fn == nil {
			return nil
		}
		if (s.IsUpdate || s.IsReuseMutable) && !g.mutable[s.Name] {
			return fmt.Errorf("cannot update immutable variable '%s' (use <- only for mutable variables)", s.Name)
		}
		if lam, ok := s.Value.(*LambdaExpr); ok {
			return g.closure(lam, s.Name)
		}
		if err := g.expr(s.Value); err != nil {
			return err
		}
		return g.storeVar(rvFA0, s.Name)
	case *MultipleAssignStmt:
		return g.multiAssign(s)
	case *LoopStmt:
		if r, ok := s.Iterable.(*RangeExpr); ok {
			return g.rangeLoop(s, r)
		}
		return g.containerLoop(s)
	case *WhileStmt:
		return g.whileLoop(s)
	case *IfStmt:
		end := g.newLabel()
		for _, b := range s.Branches {
			next := g.newLabel()
			if err := g.truthy(b.Condition); err != nil {
				return err
			}
			g.beqz(rvA0, next)
			if err := g.stmts(b.Body); err != nil {
				return err
			}
			g.j(end)
			g.bind(next)
		}
		if err := g.stmts(s.ElseBody); err != nil {
			return err
		}
		g.bind(end)
		return nil
	case *JumpStmt:
		return g.jump(s.Label, s.IsBreak, s.Value)
	case *MapUpdateStmt:
		if !g.mutable[s.MapName] {
			return fmt.Errorf("cannot modify immutable list '%s'", s.MapName)
		}
		if s.KeyName != "" {
			if f := g.lookupField(g.varStructs[s.MapName], s.KeyName); f != nil && g.varStructs[s.MapName] != "" {
				return g.fieldStore(s, f)
			}
		}
		return g.update(s)
	case *ArenaStmt:
		return g.stmts(s.Body)
	case *WithStmt:
		return g.stmts(s.Body)
	case *DeferStmt:
		for _, d := range g.sc.defers {
			if d.call == s.Call {
				g.li(rvT0, 1)
				g.sdx(rvT0, rvS0, d.flag)
				return nil
			}
		}
		return fmt.Errorf("riscv64: defer outside of a function body")
	case *ReceiveLoopStmt:
		return g.receiveLoop(s)
	case *CStructDecl, *CImportStmt:
		return nil
	}
	return fmt.Errorf("riscv64: %T statements are not supported yet", s)
}

func (g *rvGen) postfix(p *PostfixExpr) error {
	id, ok := p.Operand.(*IdentExpr)
	if !ok {
		return fmt.Errorf("riscv64: %s needs a variable", p.Operator)
	}
	if !g.mutable[id.Name] {
		return fmt.Errorf("cannot modify immutable variable '%s'", id.Name)
	}
	op := "+"
	if p.Operator == "--" {
		op = "-"
	}
	if err := g.expr(&BinaryExpr{Left: id, Operator: op, Right: &NumberExpr{Value: 1}}); err != nil {
		return err
	}
	return g.storeVar(rvFA0, id.Name)
}

func (g *rvGen) multiAssign(s *MultipleAssignStmt) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	t := g.tmp()
	if err := g.expr(s.Value); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, t)
	for i, name := range s.Names {
		g.fldx(rvFA0, rvS0, t)
		g.number1(rvFA1, float64(i))
		g.numCall(valIndex)
		if err := g.storeVar(rvFA0, name); err != nil {
			return err
		}
	}
	return nil
}

func (g *rvGen) jump(label int, isBreak bool, value Expression) error {
	if label == -1 || label == 0 && !isBreak {
		label = len(g.sc.loops)
	}
	if label == 0 {
		if value != nil {
			if err := g.expr(value); err != nil {
				return err
			}
		}
		g.j(g.sc.ret)
		return nil
	}
	if label < 1 || label > len(g.sc.loops) {
		return fmt.Errorf("riscv64: jump to loop @%d outside of it", label)
	}
	lp := g.sc.loops[label-1]
	if isBreak {
		g.j(lp.brk)
	} else {
		g.j(lp.cont)
	}
	return nil
}

func (g *rvGen) loopBody(lp rvLoop, body []Statement) error {
	g.sc.loops = append(g.sc.loops, lp)
	defer func() { g.sc.loops = g.sc.loops[:len(g.sc.loops)-1] }()
	return g.stmts(body)
}

func (g *rvGen) rangeLoop(s *LoopStmt, r *RangeExpr) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	cnt, end := g.intLoc(), g.intLoc()
	defer g.releaseInt(cnt)
	defer g.releaseInt(end)
	iters := g.tmp()
	capped := s.MaxIterations > 0 && s.MaxIterations != math.MaxInt64
	if n, ok := rvStaticCount(r); ok && n <= s.MaxIterations {
		capped = false
	}
	if capped {
		g.sdx(0, rvS0, iters)
	}
	if err := g.expr(r.Start); err != nil {
		return err
	}
	g.toI64()
	g.sdi(rvA0, cnt)
	if err := g.expr(r.End); err != nil {
		return err
	}
	g.toI64()
	if r.Inclusive {
		g.addi(rvA0, rvA0, 1)
	}
	g.sdi(rvA0, end)
	top, lp := g.newLabel(), rvLoop{g.newLabel(), g.newLabel()}
	g.bind(top)
	c := g.intReg(cnt, rvT0)
	g.br(5, c, g.intReg(end, rvT1), lp.brk)
	if capped {
		g.ldx(rvT1, rvS0, iters)
		g.li(rvT2, s.MaxIterations)
		g.br(5, rvT1, rvT2, lp.brk)
		g.addi(rvT1, rvT1, 1)
		g.sdx(rvT1, rvS0, iters)
	}
	g.fcvtDL(rvFA0, c)
	if err := g.storeVar(rvFA0, s.Iterator); err != nil {
		return err
	}
	if err := g.loopBody(lp, s.Body); err != nil {
		return err
	}
	g.bind(lp.cont)
	c = g.intReg(cnt, rvT0)
	step := rangeStepValue(r)
	if step <= 2047 {
		g.addi(c, c, int32(step))
	} else {
		g.li(rvT1, step)
		g.add(c, c, rvT1)
	}
	g.sdi(c, cnt)
	g.j(top)
	g.bind(lp.brk)
	return nil
}

// rvStaticCount is the number of iterations of a range with literal bounds.
func rvStaticCount(r *RangeExpr) (int64, bool) {
	start, ok1 := r.Start.(*NumberExpr)
	end, ok2 := r.End.(*NumberExpr)
	if !ok1 || !ok2 || start.Exact != nil || end.Exact != nil {
		return 0, false
	}
	hi := int64(end.Value)
	if r.Inclusive {
		hi++
	}
	step := rangeStepValue(r)
	return max((hi-int64(start.Value)+step-1)/step, 0), true
}

// containerLoop walks the entries of a string, list or map; any other value
// iterates once, as the single-entry map a number is.
func (g *rvGen) containerLoop(s *LoopStmt) error {
	if g.cstruct(s.IteratorType) != nil {
		g.sc.structs[s.Iterator] = s.IteratorType
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	i, n := g.intLoc(), g.intLoc()
	defer g.releaseInt(i)
	defer g.releaseInt(n)
	p := g.tmp()
	if err := g.expr(s.Iterable); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, p)
	g.sdi(0, i)
	single, start := g.newLabel(), g.newLabel()
	g.fmvXD(rvT0, rvFA0)
	g.boxTest(rvT0, rvT1, single)
	g.fld(rvFT0, rvT0, 8)
	g.fcvtLD(rvT1, rvFT0, 1)
	g.sdi(rvT1, n)
	g.j(start)
	g.bind(single)
	g.li(rvT1, -1)
	g.sdi(rvT1, n)
	g.bind(start)
	top, lp := g.newLabel(), rvLoop{g.newLabel(), g.newLabel()}
	g.bind(top)
	ir, nr := g.intReg(i, rvT1), g.intReg(n, rvT2)
	notSingle, have := g.newLabel(), g.newLabel()
	g.bge0(nr, notSingle)
	g.bnez(ir, lp.brk)
	g.fldx(rvFA0, rvS0, p)
	g.j(have)
	g.bind(notSingle)
	g.br(5, ir, nr, lp.brk)
	g.ldx(rvT0, rvS0, p)
	g.slli(rvT2, ir, 4)
	g.add(rvT0, rvT0, rvT2)
	g.fld(rvFA0, rvT0, 24)
	g.bind(have)
	if err := g.storeVar(rvFA0, s.Iterator); err != nil {
		return err
	}
	if s.KeyVar != "" {
		g.fldx(rvFA0, rvS0, p)
		g.fcvtDL(rvFA1, g.intReg(i, rvT0))
		g.numCall(valKeyAt)
		if err := g.storeVar(rvFA0, s.KeyVar); err != nil {
			return err
		}
	}
	if err := g.loopBody(lp, s.Body); err != nil {
		return err
	}
	g.bind(lp.cont)
	ir = g.intReg(i, rvT0)
	g.addi(ir, ir, 1)
	g.sdi(ir, i)
	g.j(top)
	g.bind(lp.brk)
	return nil
}

func (g *rvGen) bge0(rs uint32, l *rvLabel) { g.br(5, rs, 0, l) }

func (g *rvGen) whileLoop(s *WhileStmt) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	bounded := s.MaxIterations != math.MaxInt64
	cnt := g.tmp()
	if bounded {
		g.sdx(0, rvS0, cnt)
	}
	lp := rvLoop{g.newLabel(), g.newLabel()}
	g.bind(lp.cont)
	if bounded {
		g.ldx(rvT0, rvS0, cnt)
		g.li(rvT1, s.MaxIterations)
		g.br(5, rvT0, rvT1, lp.brk)
		g.addi(rvT0, rvT0, 1)
		g.sdx(rvT0, rvS0, cnt)
	}
	if err := g.truthy(s.Condition); err != nil {
		return err
	}
	g.beqz(rvA0, lp.brk)
	if err := g.loopBody(lp, s.Body); err != nil {
		return err
	}
	g.j(lp.cont)
	g.bind(lp.brk)
	return nil
}

// Closures: [4.0][code][capture count][captured values...]. Calls pass the
// closure in a0; the callee keeps it in its env slot.

func (g *rvGen) freeVars(lam *LambdaExpr) []string {
	bound := map[string]bool{}
	for _, p := range lam.Params {
		bound[p] = true
	}
	rvCollectNames(lam.Body, func(name string, update bool) {
		if !update {
			bound[name] = true
		}
	})
	var free []string
	seen := map[string]bool{}
	use := func(name string) {
		if !bound[name] && !seen[name] {
			seen[name] = true
			free = append(free, name)
		}
	}
	rvVisit(lam.Body, false, func(n any) {
		switch e := n.(type) {
		case *IdentExpr:
			use(e.Name)
		case *CallExpr:
			use(e.Function)
		case *AssignStmt:
			if e.IsUpdate {
				use(e.Name)
			}
		case *MapUpdateStmt:
			use(e.MapName)
		case *LambdaExpr:
			for _, name := range g.freeVars(e) {
				use(name)
			}
		}
	})
	return free
}

func (g *rvGen) closure(lam *LambdaExpr, self string) error {
	if lam.VariadicParam != "" {
		return fmt.Errorf("riscv64: variadic lambdas are not supported yet")
	}
	g.nlambda++
	f := &rvFunc{lam: lam, name: fmt.Sprintf("lambda%d", g.nlambda), params: lam.Params, body: lam.Body, label: g.newLabel()}
	selfSlot := -1
	for _, name := range g.freeVars(lam) {
		_, local := g.sc.vars[name]
		_, env := g.sc.env[name]
		if local || env {
			if name == self {
				selfSlot = len(f.captures)
			}
			f.captures = append(f.captures, rvCapture{name, &IdentExpr{Name: name}})
		}
	}
	if self != "" {
		f.name = self
	}
	if err := g.makeClosure(f); err != nil {
		return err
	}
	if self == "" {
		return nil
	}
	if selfSlot >= 0 {
		g.fmvXD(rvT0, rvFA0)
		g.fsd(rvFA0, rvT0, int32(24+8*selfSlot))
	}
	return g.storeVar(rvFA0, self)
}

func (g *rvGen) makeClosure(f *rvFunc) error {
	g.pending = append(g.pending, f)
	if len(f.captures) == 0 {
		g.la(rvT0, g.staticClosure(f))
		g.fmvDX(rvFA0, rvT0)
		return nil
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	p := g.tmp()
	g.li(rvA1, int64(24+8*len(f.captures)))
	g.call(g.l["alloc"])
	g.sdx(rvA0, rvS0, p)
	g.li(rvT0, int64(math.Float64bits(rvTagFunc)))
	g.sd(rvT0, rvA0, 0)
	g.la(rvT0, f.label)
	g.sd(rvT0, rvA0, 8)
	g.li(rvT0, int64(len(f.captures)))
	g.sd(rvT0, rvA0, 16)
	for i, c := range f.captures {
		if err := g.expr(c.val); err != nil {
			return err
		}
		g.ldx(rvT0, rvS0, p)
		g.fsd(rvFA0, rvT0, int32(24+8*i))
	}
	g.ldx(rvT0, rvS0, p)
	g.fmvDX(rvFA0, rvT0)
	return nil
}
