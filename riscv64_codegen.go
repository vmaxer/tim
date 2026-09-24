package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The riscv64 backend emits a static, libc-free ELF with a single RWX segment:
// [ELF header][code: _start, helpers, functions, numrt blob][rodata][data].
// Values are NaN-boxed doubles held in fa0; temporaries and locals live in
// s0-relative frame slots, and globals in the s1-relative data area.

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
	rvHeader               = 64 + 56
	rvMaxSlots             = 250
)

type rvLabel struct{ pos int }

type rvFixup struct {
	at   int
	l    *rvLabel
	add  int
	pair bool // auipc+addi rather than jal
}

type rvFunc struct {
	name       string
	params     []string
	paramTypes []string
	body       Expression
	label      *rvLabel
}

type rvScope struct {
	fn         *rvFunc
	vars       map[string]int32
	types      map[string]string
	nvars      int
	ntmp       int
	maxTmp     int
	prologue   int
	ret        *rvLabel
	bodyStart  *rvLabel
	loops      []rvLoop
	tailExpr   Expression
	isTopLevel bool
}

type rvLoop struct{ cont, brk *rvLabel }

type rvData struct {
	l     *rvLabel
	bytes []byte
}

type rvGen struct {
	code        []byte
	fixups      []rvFixup
	rodata      []rvData
	globals     map[string]int32
	globalTypes map[string]string
	funcs       map[string]*rvFunc
	funcOrder   []*rvFunc
	sc          *rvScope
	typing      map[string]bool
	l           map[string]*rvLabel
}

func compileRV64(program *Program) ([]byte, error) {
	g := &rvGen{
		globals:     map[string]int32{},
		globalTypes: map[string]string{},
		funcs:       map[string]*rvFunc{},
		typing:      map[string]bool{},
		l:           map[string]*rvLabel{},
	}
	for _, name := range []string{"num", "alloc", "putc", "flush", "print_raw", "print_tstr", "print_i64", "print_fixed", "strcat", "listcat", "listrep", "print_list", "blob", "globals", "heap", "outlen", "outbuf", "main"} {
		g.l[name] = g.newLabel()
	}
	var top []Statement
	for _, s := range program.Statements {
		if a, ok := s.(*AssignStmt); ok {
			if lam, ok := a.Value.(*LambdaExpr); ok {
				f := &rvFunc{name: a.Name, params: lam.Params, paramTypes: make([]string, len(lam.Params)), body: lam.Body, label: g.newLabel()}
				g.funcs[a.Name] = f
				g.funcOrder = append(g.funcOrder, f)
				continue
			}
		}
		top = append(top, s)
	}
	for _, s := range top {
		rvCollectNames(s, func(name string, _ bool) {
			if _, ok := g.globals[name]; !ok && g.funcs[name] == nil {
				g.globals[name] = int32(8 * len(g.globals))
			}
		})
	}
	if len(g.globals) > 255 {
		return nil, fmt.Errorf("riscv64: more than 255 global variables")
	}

	g.emitStart()
	g.emitHelpers()

	g.bind(g.l["main"])
	if err := g.compileBody(nil, func() error {
		for _, s := range top {
			if err := g.stmt(s); err != nil {
				return err
			}
		}
		if f := g.funcs["main"]; f != nil && len(f.params) == 0 && !(&TimCompiler{}).detectMainCallInTopLevel(top) {
			return g.userCall(f, &CallExpr{Function: "main"})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for _, f := range g.funcOrder {
		g.bind(f.label)
		if err := g.compileBody(f, func() error { return g.inTail(f.body, true) }); err != nil {
			return nil, fmt.Errorf("riscv64: in %s: %w", f.name, err)
		}
	}

	for len(g.code)%16 != 0 {
		g.w(0x00000013)
	}
	g.bind(g.l["blob"])
	g.code = append(g.code, numRuntimeRISCV64...)
	for _, d := range g.rodata {
		g.align(8)
		g.bind(d.l)
		g.code = append(g.code, d.bytes...)
	}
	g.align(8)
	for _, d := range []struct {
		name string
		size int
	}{{"globals", 8 * len(g.globals)}, {"heap", 16}, {"outlen", 8}, {"outbuf", 4096}} {
		g.bind(g.l[d.name])
		g.code = append(g.code, make([]byte, d.size)...)
	}

	for _, f := range g.fixups {
		off := f.l.pos + f.add - f.at
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
	return g.elf(), nil
}

func (g *rvGen) elf() []byte {
	size := uint64(rvHeader + len(g.code))
	b := []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	le := binary.LittleEndian
	b = le.AppendUint16(b, 2)   // ET_EXEC
	b = le.AppendUint16(b, 243) // EM_RISCV
	b = le.AppendUint32(b, 1)
	b = le.AppendUint64(b, rvBase+rvHeader)
	b = le.AppendUint64(b, 64)
	b = le.AppendUint64(b, 0)
	b = le.AppendUint32(b, 0x5) // RVC, double-float ABI
	b = le.AppendUint16(b, 64)
	b = le.AppendUint16(b, 56)
	b = le.AppendUint16(b, 1)
	b = le.AppendUint16(b, 64)
	b = le.AppendUint16(b, 0)
	b = le.AppendUint16(b, 0)
	b = le.AppendUint32(b, 1) // PT_LOAD
	b = le.AppendUint32(b, 7) // RWX
	b = le.AppendUint64(b, 0)
	b = le.AppendUint64(b, rvBase)
	b = le.AppendUint64(b, rvBase)
	b = le.AppendUint64(b, size)
	b = le.AppendUint64(b, size)
	b = le.AppendUint64(b, 0x1000)
	return append(b, g.code...)
}

// rvCollectNames reports every variable a statement list assigns (update is
// set for <-), without descending into lambdas.
func rvCollectNames(node any, add func(name string, update bool)) {
	switch n := node.(type) {
	case *AssignStmt:
		add(n.Name, n.IsUpdate)
		rvCollectNames(n.Value, add)
	case *ExpressionStmt:
		rvCollectNames(n.Expr, add)
	case *LoopStmt:
		add(n.Iterator, false)
		rvCollectNames(n.Iterable, add)
		for _, s := range n.Body {
			rvCollectNames(s, add)
		}
	case *WhileStmt:
		rvCollectNames(n.Condition, add)
		for _, s := range n.Body {
			rvCollectNames(s, add)
		}
	case *IfStmt:
		for _, b := range n.Branches {
			rvCollectNames(b.Condition, add)
			for _, s := range b.Body {
				rvCollectNames(s, add)
			}
		}
		for _, s := range n.ElseBody {
			rvCollectNames(s, add)
		}
	case *BlockExpr:
		for _, s := range n.Statements {
			rvCollectNames(s, add)
		}
	case *MatchExpr:
		rvCollectNames(n.Condition, add)
		for _, c := range n.Clauses {
			rvCollectNames(c.Guard, add)
			rvCollectNames(c.Result, add)
		}
		rvCollectNames(n.DefaultExpr, add)
	case *MapUpdateStmt:
		rvCollectNames(n.Index, add)
		rvCollectNames(n.Value, add)
	case *JumpStmt:
		rvCollectNames(n.Value, add)
	case *BinaryExpr:
		rvCollectNames(n.Left, add)
		rvCollectNames(n.Right, add)
	case *UnaryExpr:
		rvCollectNames(n.Operand, add)
	case *CastExpr:
		rvCollectNames(n.Expr, add)
	case *CallExpr:
		for _, a := range n.Args {
			rvCollectNames(a, add)
		}
	case *FStringExpr:
		for _, p := range n.Parts {
			rvCollectNames(p, add)
		}
	case *ListExpr:
		for _, e := range n.Elements {
			rvCollectNames(e, add)
		}
	case *IndexExpr:
		rvCollectNames(n.List, add)
		rvCollectNames(n.Index, add)
	case *FMAExpr:
		rvCollectNames(n.A, add)
		rvCollectNames(n.B, add)
		rvCollectNames(n.C, add)
	}
}

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

func (g *rvGen) w(x uint32)                      { g.code = binary.LittleEndian.AppendUint32(g.code, x) }
func (g *rvGen) get(at int) uint32               { return binary.LittleEndian.Uint32(g.code[at:]) }
func (g *rvGen) put(at int, x uint32)            { binary.LittleEndian.PutUint32(g.code[at:], x) }
func (g *rvGen) newLabel() *rvLabel              { return &rvLabel{pos: -1} }
func (g *rvGen) bind(l *rvLabel)                 { l.pos = len(g.code) }
func (g *rvGen) align(n int)                     { g.code = append(g.code, make([]byte, (n-len(g.code)%n)%n)...) }
func (g *rvGen) addi(rd, rs uint32, i int32)     { g.w(rvI(i, rs, 0, rd, 0x13)) }
func (g *rvGen) andi(rd, rs uint32, i int32)     { g.w(rvI(i, rs, 7, rd, 0x13)) }
func (g *rvGen) xori(rd, rs uint32, i int32)     { g.w(rvI(i, rs, 4, rd, 0x13)) }
func (g *rvGen) slli(rd, rs uint32, sh int32)    { g.w(rvI(sh, rs, 1, rd, 0x13)) }
func (g *rvGen) srli(rd, rs uint32, sh int32)    { g.w(rvI(sh, rs, 5, rd, 0x13)) }
func (g *rvGen) mv(rd, rs uint32)                { g.addi(rd, rs, 0) }
func (g *rvGen) op(f7, f3, rd, rs1, rs2 uint32)  { g.w(rvR(f7, rs2, rs1, f3, rd, 0x33)) }
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
func (g *rvGen) ret()                            { g.w(rvI(0, rvRA, 0, 0, 0x67)) }

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
func (g *rvGen) br(f3, rs1, rs2 uint32, l *rvLabel) {
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

// Runtime.

// emitStart runs the top level and exits with the value of its last expression.
func (g *rvGen) emitStart() {
	skip := g.newLabel()
	g.la(rvS1, g.l["globals"])
	g.call(g.l["main"])
	g.li(rvS2, 0)
	g.feq(rvT0, rvFA0, rvFA0)
	g.beqz(rvT0, skip)
	g.fcvtLD(rvS2, rvFA0, 1)
	g.bind(skip)
	g.call(g.l["flush"])
	g.mv(rvA0, rvS2)
	g.li(rvA7, 93)
	g.ecall()
}

func (g *rvGen) emitHelpers() {
	l := g.l

	// num(a0 = op, fa0, fa1) -> fa0
	g.bind(l["num"])
	g.addi(rvSP, rvSP, -16)
	g.sd(rvRA, rvSP, 8)
	g.fmvXD(rvA1, rvFA0)
	g.fmvXD(rvA2, rvFA1)
	g.la(rvA3, l["alloc"])
	g.li(rvA4, 0)
	g.jal(rvRA, l["blob"], numRuntimeRISCV64Entry)
	g.fmvDX(rvFA0, rvA0)
	g.ld(rvRA, rvSP, 8)
	g.addi(rvSP, rvSP, 16)
	g.ret()

	// alloc(ctx, a1 = size) -> a0: bump allocation from mmap'd chunks.
	g.bind(l["alloc"])
	grow, skip := g.newLabel(), g.newLabel()
	g.addi(rvA1, rvA1, 15)
	g.andi(rvA1, rvA1, -16)
	g.la(rvT0, l["heap"])
	g.ld(rvT1, rvT0, 0)
	g.ld(rvT2, rvT0, 8)
	g.op(0, 0, rvT3, rvT1, rvA1)
	g.br(6, rvT2, rvT3, grow)
	g.sd(rvT3, rvT0, 0)
	g.mv(rvA0, rvT1)
	g.ret()
	g.bind(grow)
	g.mv(rvT4, rvA1)
	g.li(rvT5, 64<<20)
	g.br(7, rvT5, rvT4, skip)
	g.mv(rvT5, rvT4)
	g.bind(skip)
	g.mv(rvA1, rvT5)
	g.li(rvA0, 0)
	g.li(rvA2, 3)
	g.li(rvA3, 0x22)
	g.li(rvA4, -1)
	g.li(rvA5, 0)
	g.li(rvA7, 222)
	g.ecall()
	g.op(0, 0, rvT2, rvA0, rvT5)
	g.sd(rvT2, rvT0, 8)
	g.op(0, 0, rvT3, rvA0, rvT4)
	g.sd(rvT3, rvT0, 0)
	g.ret()

	// putc(a0): buffered stdout. Clobbers t0-t2, a0-a2, a7.
	g.bind(l["putc"])
	done := g.newLabel()
	g.la(rvT0, l["outlen"])
	g.ld(rvT1, rvT0, 0)
	g.la(rvT2, l["outbuf"])
	g.op(0, 0, rvT2, rvT2, rvT1)
	g.sb(rvA0, rvT2, 0)
	g.addi(rvT1, rvT1, 1)
	g.sd(rvT1, rvT0, 0)
	g.li(rvT2, 4096)
	g.br(6, rvT1, rvT2, done)
	g.j(l["flush"])
	g.bind(done)
	g.ret()

	g.bind(l["flush"])
	done = g.newLabel()
	g.la(rvT0, l["outlen"])
	g.ld(rvA2, rvT0, 0)
	g.beqz(rvA2, done)
	g.li(rvA0, 1)
	g.la(rvA1, l["outbuf"])
	g.li(rvA7, 64)
	g.ecall()
	g.sd(0, rvT0, 0)
	g.bind(done)
	g.ret()

	// print_raw(a0 = bytes, a1 = len)
	g.bind(l["print_raw"])
	loop, done := g.newLabel(), g.newLabel()
	g.addi(rvSP, rvSP, -16)
	g.sd(rvRA, rvSP, 8)
	g.mv(rvT3, rvA1)
	g.mv(rvT4, rvA0)
	g.bind(loop)
	g.beqz(rvT3, done)
	g.lbu(rvA0, rvT4, 0)
	g.call(l["putc"])
	g.addi(rvT4, rvT4, 1)
	g.addi(rvT3, rvT3, -1)
	g.j(loop)
	g.bind(done)
	g.ld(rvRA, rvSP, 8)
	g.addi(rvSP, rvSP, 16)
	g.ret()

	// print_tstr(a0 = Tim string [count][key0][char0]...)
	g.bind(l["print_tstr"])
	loop, done = g.newLabel(), g.newLabel()
	g.addi(rvSP, rvSP, -16)
	g.sd(rvRA, rvSP, 8)
	g.fld(rvFT0, rvA0, 0)
	g.fcvtLD(rvT3, rvFT0, 1)
	g.addi(rvT4, rvA0, 16)
	g.bind(loop)
	g.beqz(rvT3, done)
	g.fld(rvFT0, rvT4, 0)
	g.fcvtLD(rvA0, rvFT0, 1)
	g.call(l["putc"])
	g.addi(rvT4, rvT4, 16)
	g.addi(rvT3, rvT3, -1)
	g.j(loop)
	g.bind(done)
	g.ld(rvRA, rvSP, 8)
	g.addi(rvSP, rvSP, 16)
	g.ret()

	// print_i64(a0)
	g.bind(l["print_i64"])
	pos, digits, out, done := g.newLabel(), g.newLabel(), g.newLabel(), g.newLabel()
	g.addi(rvSP, rvSP, -48)
	g.sd(rvRA, rvSP, 40)
	g.mv(rvT3, rvA0)
	g.br(5, rvT3, 0, pos)
	g.li(rvA0, '-')
	g.call(l["putc"])
	g.op(0x20, 0, rvT3, 0, rvT3)
	g.bind(pos)
	g.addi(rvT4, rvSP, 32)
	g.li(rvT5, 10)
	g.bind(digits)
	g.op(1, 7, rvT6, rvT3, rvT5)
	g.op(1, 5, rvT3, rvT3, rvT5)
	g.addi(rvT6, rvT6, '0')
	g.addi(rvT4, rvT4, -1)
	g.sb(rvT6, rvT4, 0)
	g.bnez(rvT3, digits)
	g.bind(out)
	g.addi(rvT5, rvSP, 32)
	g.br(0, rvT4, rvT5, done)
	g.lbu(rvA0, rvT4, 0)
	g.call(l["putc"])
	g.addi(rvT4, rvT4, 1)
	g.j(out)
	g.bind(done)
	g.ld(rvRA, rvSP, 40)
	g.addi(rvSP, rvSP, 48)
	g.ret()

	// print_fixed(fa0, a0 = precision): fixed-point with round-to-nearest-even digits.
	g.bind(l["print_fixed"])
	pos, pow, carry, frac, done := g.newLabel(), g.newLabel(), g.newLabel(), g.newLabel(), g.newLabel()
	g.addi(rvSP, rvSP, -48)
	g.sd(rvRA, rvSP, 40)
	g.fsd(rvFS0, rvSP, 32)
	g.sd(rvS2, rvSP, 24)
	g.sd(rvS3, rvSP, 16)
	g.fmvD(rvFS0, rvFA0)
	g.mv(rvS2, rvA0)
	g.fmvDX(rvFT0, 0)
	g.fop(0x51, 1, rvT0, rvFS0, rvFT0)
	g.beqz(rvT0, pos)
	g.li(rvA0, '-')
	g.call(l["putc"])
	g.fop(0x11, 1, rvFS0, rvFS0, rvFS0)
	g.bind(pos)
	g.fcvtLD(rvS3, rvFS0, 1)
	g.li(rvT0, 1)
	g.li(rvT2, 10)
	g.bind(pow)
	g.beqz(rvS2, carry)
	g.op(1, 0, rvT0, rvT0, rvT2)
	g.addi(rvS2, rvS2, -1)
	g.j(pow)
	g.bind(carry)
	g.fcvtDL(rvFT1, rvS3)
	g.fop(0x05, 7, rvFT1, rvFS0, rvFT1)
	g.fcvtDL(rvFT2, rvT0)
	g.fop(0x09, 7, rvFT1, rvFT1, rvFT2)
	g.fcvtLD(rvT1, rvFT1, 0)
	g.br(6, rvT1, rvT0, frac)
	g.addi(rvS3, rvS3, 1)
	g.op(0x20, 0, rvT1, rvT1, rvT0)
	g.bind(frac)
	g.sd(rvT0, rvSP, 8)
	g.sd(rvT1, rvSP, 0)
	g.mv(rvA0, rvS3)
	g.call(l["print_i64"])
	g.ld(rvS2, rvSP, 8)
	g.ld(rvS3, rvSP, 0)
	g.li(rvT0, 1)
	g.br(0, rvS2, rvT0, done)
	g.li(rvA0, '.')
	g.call(l["putc"])
	loop = g.newLabel()
	g.bind(loop)
	g.li(rvT0, 10)
	g.op(1, 5, rvS2, rvS2, rvT0)
	g.beqz(rvS2, done)
	g.op(1, 5, rvA0, rvS3, rvS2)
	g.op(1, 7, rvA0, rvA0, rvT0)
	g.addi(rvA0, rvA0, '0')
	g.call(l["putc"])
	g.j(loop)
	g.bind(done)
	g.ld(rvRA, rvSP, 40)
	g.fld(rvFS0, rvSP, 32)
	g.ld(rvS2, rvSP, 24)
	g.ld(rvS3, rvSP, 16)
	g.addi(rvSP, rvSP, 48)
	g.ret()

	// strcat(a0, a1) -> a0: new Tim string with the second string's keys renumbered.
	g.bind(l["strcat"])
	g.addi(rvSP, rvSP, -32)
	g.sd(rvRA, rvSP, 24)
	g.sd(rvS2, rvSP, 16)
	g.sd(rvS3, rvSP, 8)
	g.mv(rvS2, rvA0)
	g.mv(rvS3, rvA1)
	g.fld(rvFT0, rvS2, 0)
	g.fld(rvFT1, rvS3, 0)
	g.fop(0x01, 7, rvFT0, rvFT0, rvFT1)
	g.fsd(rvFT0, rvSP, 0)
	g.fcvtLD(rvA1, rvFT0, 1)
	g.slli(rvA1, rvA1, 4)
	g.addi(rvA1, rvA1, 8)
	g.call(l["alloc"])
	g.fld(rvFT0, rvSP, 0)
	g.fsd(rvFT0, rvA0, 0)
	g.addi(rvT2, rvA0, 8)
	g.li(rvT3, 0)
	for _, src := range []uint32{rvS2, rvS3} {
		loop, next := g.newLabel(), g.newLabel()
		g.fld(rvFT0, src, 0)
		g.fcvtLD(rvT0, rvFT0, 1)
		g.addi(rvT1, src, 16)
		g.bind(loop)
		g.beqz(rvT0, next)
		g.sd(rvT3, rvT2, 0)
		g.fld(rvFT0, rvT1, 0)
		g.fsd(rvFT0, rvT2, 8)
		g.addi(rvT1, rvT1, 16)
		g.addi(rvT2, rvT2, 16)
		g.addi(rvT3, rvT3, 1)
		g.addi(rvT0, rvT0, -1)
		g.j(loop)
		g.bind(next)
	}
	g.ld(rvRA, rvSP, 24)
	g.ld(rvS2, rvSP, 16)
	g.ld(rvS3, rvSP, 8)
	g.addi(rvSP, rvSP, 32)
	g.ret()

	// listcat(a0, a1) -> a0: flat lists [count][elem0][elem1]...
	g.bind(l["listcat"])
	g.addi(rvSP, rvSP, -32)
	g.sd(rvRA, rvSP, 24)
	g.sd(rvS2, rvSP, 16)
	g.sd(rvS3, rvSP, 8)
	g.mv(rvS2, rvA0)
	g.mv(rvS3, rvA1)
	g.fld(rvFT0, rvS2, 0)
	g.fld(rvFT1, rvS3, 0)
	g.fop(0x01, 7, rvFT0, rvFT0, rvFT1)
	g.fsd(rvFT0, rvSP, 0)
	g.fcvtLD(rvA1, rvFT0, 1)
	g.slli(rvA1, rvA1, 3)
	g.addi(rvA1, rvA1, 8)
	g.call(l["alloc"])
	g.fld(rvFT0, rvSP, 0)
	g.fsd(rvFT0, rvA0, 0)
	g.addi(rvT2, rvA0, 8)
	for _, src := range []uint32{rvS2, rvS3} {
		loop, next := g.newLabel(), g.newLabel()
		g.fld(rvFT0, src, 0)
		g.fcvtLD(rvT0, rvFT0, 1)
		g.addi(rvT1, src, 8)
		g.bind(loop)
		g.beqz(rvT0, next)
		g.fld(rvFT0, rvT1, 0)
		g.fsd(rvFT0, rvT2, 0)
		g.addi(rvT1, rvT1, 8)
		g.addi(rvT2, rvT2, 8)
		g.addi(rvT0, rvT0, -1)
		g.j(loop)
		g.bind(next)
	}
	g.ld(rvRA, rvSP, 24)
	g.ld(rvS2, rvSP, 16)
	g.ld(rvS3, rvSP, 8)
	g.addi(rvSP, rvSP, 32)
	g.ret()

	// listrep(a0 = list, a1 = times) -> a0
	g.bind(l["listrep"])
	outer, inner, next, rdone, nonneg := g.newLabel(), g.newLabel(), g.newLabel(), g.newLabel(), g.newLabel()
	g.addi(rvSP, rvSP, -48)
	g.sd(rvRA, rvSP, 40)
	g.sd(rvS2, rvSP, 32)
	g.sd(rvS3, rvSP, 24)
	g.sd(rvS4, rvSP, 16)
	g.mv(rvS2, rvA0)
	g.mv(rvS3, rvA1)
	g.br(5, rvS3, 0, nonneg)
	g.li(rvS3, 0)
	g.bind(nonneg)
	g.fld(rvFT0, rvS2, 0)
	g.fcvtLD(rvS4, rvFT0, 1)
	g.op(1, 0, rvA1, rvS4, rvS3)
	g.sd(rvA1, rvSP, 0)
	g.slli(rvA1, rvA1, 3)
	g.addi(rvA1, rvA1, 8)
	g.call(l["alloc"])
	g.ld(rvT0, rvSP, 0)
	g.fcvtDL(rvFT0, rvT0)
	g.fsd(rvFT0, rvA0, 0)
	g.addi(rvT2, rvA0, 8)
	g.bind(outer)
	g.beqz(rvS3, rdone)
	g.mv(rvT0, rvS4)
	g.addi(rvT1, rvS2, 8)
	g.bind(inner)
	g.beqz(rvT0, next)
	g.fld(rvFT0, rvT1, 0)
	g.fsd(rvFT0, rvT2, 0)
	g.addi(rvT1, rvT1, 8)
	g.addi(rvT2, rvT2, 8)
	g.addi(rvT0, rvT0, -1)
	g.j(inner)
	g.bind(next)
	g.addi(rvS3, rvS3, -1)
	g.j(outer)
	g.bind(rdone)
	g.ld(rvRA, rvSP, 40)
	g.ld(rvS2, rvSP, 32)
	g.ld(rvS3, rvSP, 24)
	g.ld(rvS4, rvSP, 16)
	g.addi(rvSP, rvSP, 48)
	g.ret()

	// print_list(a0): [a, b, ...]
	g.bind(l["print_list"])
	first := g.newLabel()
	loop, done = g.newLabel(), g.newLabel()
	g.addi(rvSP, rvSP, -32)
	g.sd(rvRA, rvSP, 24)
	g.sd(rvS2, rvSP, 16)
	g.sd(rvS3, rvSP, 8)
	g.mv(rvS2, rvA0)
	g.li(rvS3, 0)
	g.li(rvA0, '[')
	g.call(l["putc"])
	g.bind(loop)
	g.fld(rvFT0, rvS2, 0)
	g.fcvtLD(rvT0, rvFT0, 1)
	g.br(5, rvS3, rvT0, done)
	g.beqz(rvS3, first)
	g.li(rvA0, ',')
	g.call(l["putc"])
	g.li(rvA0, ' ')
	g.call(l["putc"])
	g.bind(first)
	g.slli(rvT0, rvS3, 3)
	g.op(0, 0, rvT0, rvT0, rvS2)
	g.fld(rvFA0, rvT0, 8)
	g.li(rvA0, numOpStr)
	g.call(l["num"])
	g.fmvXD(rvA0, rvFA0)
	g.call(l["print_tstr"])
	g.addi(rvS3, rvS3, 1)
	g.j(loop)
	g.bind(done)
	g.li(rvA0, ']')
	g.call(l["putc"])
	g.ld(rvRA, rvSP, 24)
	g.ld(rvS2, rvSP, 16)
	g.ld(rvS3, rvSP, 8)
	g.addi(rvSP, rvSP, 32)
	g.ret()
}

// Functions and frames.

func (g *rvGen) compileBody(f *rvFunc, body func() error) error {
	sc := &rvScope{fn: f, vars: map[string]int32{}, types: map[string]string{}, ret: g.newLabel(), bodyStart: g.newLabel(), isTopLevel: f == nil}
	g.sc = sc
	sc.prologue = len(g.code)
	for range 4 {
		g.w(0)
	}
	if f != nil {
		if len(f.params) > 8 {
			return fmt.Errorf("more than 8 parameters")
		}
		for i, p := range f.params {
			off := sc.newVar(p)
			sc.types[p] = f.paramTypes[i]
			g.fsd(uint32(rvFA0+i), rvS0, off)
		}
		rvCollectNames(f.body, func(name string, update bool) {
			if _, ok := sc.vars[name]; ok {
				return
			}
			if _, global := g.globals[name]; !update || !global {
				sc.newVar(name)
			}
		})
		sc.tailExpr = f.body
	}
	g.bind(sc.bodyStart)
	if err := body(); err != nil {
		return err
	}
	g.bind(sc.ret)
	g.ld(rvRA, rvS0, -8)
	g.mv(rvT0, rvS0)
	g.ld(rvS0, rvS0, -16)
	g.mv(rvSP, rvT0)
	g.ret()

	slots := sc.nvars + sc.maxTmp
	if slots > rvMaxSlots {
		return fmt.Errorf("riscv64: function needs %d stack slots (max %d)", slots, rvMaxSlots)
	}
	frame := int32(16+8*slots+15) &^ 15
	g.put(sc.prologue, rvI(-frame, rvSP, 0, rvSP, 0x13))
	g.put(sc.prologue+4, rvS(frame-8, rvRA, rvSP, 3, 0x23))
	g.put(sc.prologue+8, rvS(frame-16, rvS0, rvSP, 3, 0x23))
	g.put(sc.prologue+12, rvI(frame, rvSP, 0, rvS0, 0x13))
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

// Variables.

func (g *rvGen) varRef(name string) (base uint32, off int32, err error) {
	if off, ok := g.sc.vars[name]; ok {
		return rvS0, off, nil
	}
	if off, ok := g.globals[name]; ok {
		return rvS1, off, nil
	}
	return 0, 0, fmt.Errorf("riscv64: undefined variable %s", name)
}

func (g *rvGen) varType(name string) string {
	if _, ok := g.sc.vars[name]; ok {
		return g.sc.types[name]
	}
	return g.globalTypes[name]
}

func (g *rvGen) setVarType(name, t string) {
	if _, ok := g.sc.vars[name]; ok {
		g.sc.types[name] = t
	} else {
		g.globalTypes[name] = t
	}
}

func (g *rvGen) exprType(e Expression) string {
	switch e := e.(type) {
	case *StringExpr, *FStringExpr:
		return "string"
	case *ListExpr:
		return "list"
	case *IdentExpr:
		return g.varType(e.Name)
	case *BinaryExpr:
		if l := g.exprType(e.Left); e.Operator == "+" && (l == "string" || l == "list") && g.exprType(e.Right) == l {
			return l
		}
		if e.Operator == "*" && g.exprType(e.Left) == "list" {
			return "list"
		}
	case *CastExpr:
		if e.Type == "string" || e.Type == "str" {
			return "string"
		}
	case *CallExpr:
		if f := g.funcs[e.Function]; f != nil {
			if g.typing[f.name] {
				return ""
			}
			g.typing[f.name] = true
			defer delete(g.typing, f.name)
			saved := g.sc
			g.sc = &rvScope{vars: map[string]int32{}, types: map[string]string{}}
			for i, p := range f.params {
				g.sc.vars[p] = 0
				g.sc.types[p] = f.paramTypes[i]
			}
			t := g.exprType(f.body)
			g.sc = saved
			return t
		}
		if e.Function == "str" {
			return "string"
		}
	case *MatchExpr:
		for _, c := range e.Clauses {
			if c.Result != nil {
				if t := g.exprType(c.Result); t != "" {
					return t
				}
			}
		}
		if e.DefaultExpr != nil {
			return g.exprType(e.DefaultExpr)
		}
	case *BlockExpr:
		if n := len(e.Statements); n > 0 {
			if s, ok := e.Statements[n-1].(*ExpressionStmt); ok {
				return g.exprType(s.Expr)
			}
		}
	}
	return ""
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
	case *LoopStmt, *WhileStmt, *IfStmt, *MapUpdateStmt:
		defer g.number(0)
	}
	switch s := s.(type) {
	case *ExpressionStmt:
		return g.expr(s.Expr)
	case *AssignStmt:
		if _, ok := s.Value.(*LambdaExpr); ok {
			return fmt.Errorf("riscv64: nested functions and closures are not supported")
		}
		if err := g.expr(s.Value); err != nil {
			return err
		}
		base, off, err := g.varRef(s.Name)
		if err != nil {
			return err
		}
		g.fsd(rvFA0, base, off)
		g.setVarType(s.Name, g.exprType(s.Value))
		return nil
	case *LoopStmt:
		return g.rangeLoop(s)
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
		return g.listUpdate(s)
	case *CStructDecl:
		return nil
	}
	return fmt.Errorf("riscv64: %T statements are not supported yet", s)
}

func (g *rvGen) jump(label int, isBreak bool, value Expression) error {
	if label == 0 {
		if value != nil {
			if err := g.expr(value); err != nil {
				return err
			}
		}
		g.j(g.sc.ret)
		return nil
	}
	if label > len(g.sc.loops) {
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

func (g *rvGen) rangeLoop(s *LoopStmt) error {
	r, ok := s.Iterable.(*RangeExpr)
	if !ok {
		if g.exprType(s.Iterable) == "list" {
			return g.listLoop(s)
		}
		return fmt.Errorf("riscv64: only range and list loops are supported")
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	cnt, end := g.tmp(), g.tmp()
	if err := g.expr(r.Start); err != nil {
		return err
	}
	g.toI64()
	g.sd(rvA0, rvS0, cnt)
	if err := g.expr(r.End); err != nil {
		return err
	}
	g.toI64()
	if r.Inclusive {
		g.addi(rvA0, rvA0, 1)
	}
	g.sd(rvA0, rvS0, end)
	base, off, err := g.varRef(s.Iterator)
	if err != nil {
		return err
	}
	g.setVarType(s.Iterator, "")
	top, lp := g.newLabel(), rvLoop{g.newLabel(), g.newLabel()}
	g.bind(top)
	g.ld(rvT0, rvS0, cnt)
	g.ld(rvT1, rvS0, end)
	g.br(5, rvT0, rvT1, lp.brk)
	g.fcvtDL(rvFA0, rvT0)
	g.fsd(rvFA0, base, off)
	g.sc.loops = append(g.sc.loops, lp)
	if err := g.stmts(s.Body); err != nil {
		return err
	}
	g.sc.loops = g.sc.loops[:len(g.sc.loops)-1]
	g.bind(lp.cont)
	g.ld(rvT0, rvS0, cnt)
	g.addi(rvT0, rvT0, 1)
	g.sd(rvT0, rvS0, cnt)
	g.j(top)
	g.bind(lp.brk)
	return nil
}

func (g *rvGen) whileLoop(s *WhileStmt) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	bounded := s.MaxIterations != math.MaxInt64
	cnt := g.tmp()
	if bounded {
		g.sd(0, rvS0, cnt)
	}
	lp := rvLoop{g.newLabel(), g.newLabel()}
	g.bind(lp.cont)
	if bounded {
		g.ld(rvT0, rvS0, cnt)
		g.li(rvT1, s.MaxIterations)
		g.br(5, rvT0, rvT1, lp.brk)
		g.addi(rvT0, rvT0, 1)
		g.sd(rvT0, rvS0, cnt)
	}
	if err := g.truthy(s.Condition); err != nil {
		return err
	}
	g.beqz(rvA0, lp.brk)
	g.sc.loops = append(g.sc.loops, lp)
	if err := g.stmts(s.Body); err != nil {
		return err
	}
	g.sc.loops = g.sc.loops[:len(g.sc.loops)-1]
	g.j(lp.cont)
	g.bind(lp.brk)
	return nil
}

// Expressions: the value ends up in fa0.

func (g *rvGen) inTail(e Expression, tail bool) error {
	if !tail {
		return g.expr(e)
	}
	saved := g.sc.tailExpr
	g.sc.tailExpr = e
	defer func() { g.sc.tailExpr = saved }()
	return g.expr(e)
}

func (g *rvGen) number(v float64) {
	if bits := math.Float64bits(v); bits == 0 {
		g.fmvDX(rvFA0, 0)
	} else {
		g.li(rvT0, int64(bits))
		g.fmvDX(rvFA0, rvT0)
	}
}

func (g *rvGen) timString(s string) *rvLabel {
	b := binary.LittleEndian.AppendUint64(nil, math.Float64bits(float64(len(s))))
	for i := range len(s) {
		b = binary.LittleEndian.AppendUint64(b, uint64(i))
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(float64(s[i])))
	}
	return g.constData(b)
}

func (g *rvGen) expr(e Expression) error {
	switch e := e.(type) {
	case *NumberExpr:
		if e.Exact == nil {
			g.number(e.Value)
			return nil
		}
		data, tag := numStoreInt(nil, e.Exact.Num()), int64(numTagBig)
		if !e.Exact.IsInt() {
			data, tag = numStoreInt(data, e.Exact.Denom()), numTagRat
		}
		g.la(rvT0, g.constData(data))
		g.li(rvT1, tag<<48)
		g.op(0, 6, rvT0, rvT0, rvT1)
		g.fmvDX(rvFA0, rvT0)
	case *BooleanExpr:
		g.number(map[bool]float64{true: 1}[e.Value])
	case *StringExpr:
		g.la(rvT0, g.timString(processEscapeSequences(e.Value)))
		g.fmvDX(rvFA0, rvT0)
	case *FStringExpr:
		return g.fstring(e)
	case *IdentExpr:
		if g.funcs[e.Name] != nil {
			return fmt.Errorf("riscv64: functions are not first-class values here (%s)", e.Name)
		}
		base, off, err := g.varRef(e.Name)
		if err != nil {
			return err
		}
		g.fld(rvFA0, base, off)
	case *BinaryExpr:
		return g.binary(e)
	case *UnaryExpr:
		return g.unary(e.Operator, e.Operand)
	case *LengthExpr:
		return g.unary("#", e.Operand)
	case *FMAExpr:
		var prod Expression = &BinaryExpr{Left: e.A, Operator: "*", Right: e.B}
		if e.IsNegMul {
			prod = &UnaryExpr{Operator: "-", Operand: prod}
		}
		op := "+"
		if e.IsSub {
			op = "-"
		}
		return g.expr(&BinaryExpr{Left: prod, Operator: op, Right: e.C})
	case *CastExpr:
		if err := g.expr(e.Expr); err != nil {
			return err
		}
		if (e.Type == "string" || e.Type == "str") && g.exprType(e.Expr) != "string" {
			g.numCall(numOpStr)
		}
	case *ListExpr:
		return g.list(e)
	case *IndexExpr:
		return g.index(e)
	case *MatchExpr:
		return g.match(e)
	case *BlockExpr:
		return g.block(e)
	case *JumpExpr:
		return g.jump(e.Label, e.IsBreak, e.Value)
	case *CallExpr:
		return g.callExpr(e)
	default:
		return fmt.Errorf("riscv64: %T expressions are not supported yet", e)
	}
	return nil
}

func (g *rvGen) block(e *BlockExpr) error {
	tail := g.sc.tailExpr == Expression(e)
	for i, s := range e.Statements {
		if i == len(e.Statements)-1 {
			switch s := s.(type) {
			case *ExpressionStmt:
				return g.inTail(s.Expr, tail)
			case *AssignStmt:
				if err := g.stmt(s); err != nil {
					return err
				}
				return g.expr(&IdentExpr{Name: s.Name})
			}
		}
		if err := g.stmt(s); err != nil {
			return err
		}
	}
	g.number(0)
	return nil
}

func (g *rvGen) match(e *MatchExpr) error {
	tail := g.sc.tailExpr == Expression(e)
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	cond := g.tmp()
	if err := g.expr(e.Condition); err != nil {
		return err
	}
	g.fsd(rvFA0, rvS0, cond)
	end := g.newLabel()
	for _, c := range e.Clauses {
		next := g.newLabel()
		switch {
		case c.IsValueMatch:
			if err := g.expr(c.Guard); err != nil {
				return err
			}
			g.fmvD(rvFA1, rvFA0)
			g.fld(rvFA0, rvS0, cond)
			g.numBinop("==")
			g.fmvXD(rvA0, rvFA0)
		case c.Guard != nil:
			if err := g.truthy(c.Guard); err != nil {
				return err
			}
		default:
			g.fld(rvFA0, rvS0, cond)
			g.truthyFA0()
		}
		g.beqz(rvA0, next)
		if c.Result != nil {
			if err := g.inTail(c.Result, tail); err != nil {
				return err
			}
		} else {
			g.number(0)
		}
		g.j(end)
		g.bind(next)
	}
	switch {
	case e.DefaultExpr != nil:
		if err := g.inTail(e.DefaultExpr, tail); err != nil {
			return err
		}
	case len(e.Clauses) == 0:
		g.fld(rvFA0, rvS0, cond)
	default:
		g.number(0)
	}
	g.bind(end)
	return nil
}

// operands evaluates l into a temporary and r into fa1, leaving l in fa0.
func (g *rvGen) operands(l, r Expression) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	t := g.tmp()
	if err := g.expr(l); err != nil {
		return err
	}
	g.fsd(rvFA0, rvS0, t)
	if err := g.expr(r); err != nil {
		return err
	}
	g.fmvD(rvFA1, rvFA0)
	g.fld(rvFA0, rvS0, t)
	return nil
}

func (g *rvGen) binary(e *BinaryExpr) error {
	switch e.Operator {
	case "and", "or":
		short, end := g.newLabel(), g.newLabel()
		if err := g.truthy(e.Left); err != nil {
			return err
		}
		if e.Operator == "and" {
			g.beqz(rvA0, short)
		} else {
			g.bnez(rvA0, short)
		}
		if err := g.truthy(e.Right); err != nil {
			return err
		}
		g.fcvtDL(rvFA0, rvA0)
		g.j(end)
		g.bind(short)
		g.number(map[bool]float64{true: 1}[e.Operator == "or"])
		g.bind(end)
		return nil
	case "xor":
		mark := g.sc.ntmp
		defer func() { g.sc.ntmp = mark }()
		t := g.tmp()
		if err := g.truthy(e.Left); err != nil {
			return err
		}
		g.sd(rvA0, rvS0, t)
		if err := g.truthy(e.Right); err != nil {
			return err
		}
		g.ld(rvT0, rvS0, t)
		g.op(0, 4, rvA0, rvA0, rvT0)
		g.fcvtDL(rvFA0, rvA0)
		return nil
	case "or!":
		// Errors (positive NaNs) and 0 take the right-hand side.
		useDefault, end := g.newLabel(), g.newLabel()
		if err := g.expr(e.Left); err != nil {
			return err
		}
		g.fmvXD(rvT0, rvFA0)
		g.slli(rvT1, rvT0, 1)
		g.beqz(rvT1, useDefault)
		g.srli(rvT0, rvT0, 51)
		g.li(rvT1, 0xFFF)
		g.br(1, rvT0, rvT1, end)
		g.bind(useDefault)
		if err := g.expr(e.Right); err != nil {
			return err
		}
		g.bind(end)
		return nil
	}
	if e.Operator == "*" && g.exprType(e.Left) == "list" && g.exprType(e.Right) == "" {
		mark := g.sc.ntmp
		defer func() { g.sc.ntmp = mark }()
		t := g.tmp()
		if err := g.expr(e.Left); err != nil {
			return err
		}
		g.fsd(rvFA0, rvS0, t)
		if err := g.expr(e.Right); err != nil {
			return err
		}
		g.toI64()
		g.mv(rvA1, rvA0)
		g.ld(rvA0, rvS0, t)
		g.call(g.l["listrep"])
		g.fmvDX(rvFA0, rvA0)
		return nil
	}
	if lt, rt := g.exprType(e.Left), g.exprType(e.Right); lt == "string" || rt == "string" || lt == "list" || rt == "list" {
		if e.Operator != "+" || lt != rt {
			return fmt.Errorf("riscv64: operator %s is not supported on %s and %s values", e.Operator, lt, rt)
		}
		if err := g.operands(e.Left, e.Right); err != nil {
			return err
		}
		g.fmvXD(rvA0, rvFA0)
		g.fmvXD(rvA1, rvFA1)
		g.call(g.l[map[string]string{"string": "strcat", "list": "listcat"}[lt]])
		g.fmvDX(rvFA0, rvA0)
		return nil
	}
	if numBitwiseOps[e.Operator] {
		return g.bitwise(e)
	}
	if _, ok := numBinops[e.Operator]; !ok {
		return fmt.Errorf("riscv64: operator %s is not supported yet", e.Operator)
	}
	if err := g.operands(e.Left, e.Right); err != nil {
		return err
	}
	g.numBinop(e.Operator)
	return nil
}

func (g *rvGen) numCall(op int) {
	g.li(rvA0, int64(op))
	g.call(g.l["num"])
}

// fixnumCheck branches to slow unless freg holds a non-NaN value with |x| < 2^53.
func (g *rvGen) fixnumCheck(freg uint32, slow *rvLabel) {
	g.fmvXD(rvT0, freg)
	g.slli(rvT0, rvT0, 1)
	g.srli(rvT0, rvT0, 53)
	g.li(rvT1, 0x434)
	g.br(7, rvT0, rvT1, slow)
}

func (g *rvGen) smallIntCheck(freg uint32, no *rvLabel) {
	g.fixnumCheck(freg, no)
	g.fcvtLD(rvT0, freg, 1)
	g.fcvtDL(rvFT2, rvT0)
	g.feq(rvT1, rvFT2, freg)
	g.beqz(rvT1, no)
}

// numBinop computes fa0 = fa0 op fa1 with exact semantics.
func (g *rvGen) numBinop(op string) {
	slow, done := g.newLabel(), g.newLabel()
	switch op {
	case "+", "-", "*":
		g.fop(map[string]uint32{"+": 0x01, "-": 0x05, "*": 0x09}[op], 7, rvFT0, rvFA0, rvFA1)
		g.fixnumCheck(rvFT0, slow)
		g.fmvD(rvFA0, rvFT0)
		g.j(done)
	case "/":
		notInt, inexact := g.newLabel(), g.newLabel()
		g.fop(0x0D, 7, rvFT0, rvFA0, rvFA1)
		g.smallIntCheck(rvFT0, notInt)
		g.j(inexact)
		g.bind(notInt)
		g.smallIntCheck(rvFA0, inexact)
		g.smallIntCheck(rvFA1, inexact)
		g.j(slow)
		g.bind(inexact)
		g.fmvD(rvFA0, rvFT0)
		g.j(done)
	case "%", "mod":
		g.smallIntCheck(rvFA0, slow)
		g.smallIntCheck(rvFA1, slow)
		g.fcvtLD(rvT0, rvFA0, 1)
		g.fcvtLD(rvT1, rvFA1, 1)
		g.beqz(rvT1, slow)
		g.op(1, 6, rvT0, rvT0, rvT1)
		g.fcvtDL(rvFA0, rvT0)
		g.j(done)
	case "<", "<=", ">", ">=", "==", "!=":
		g.feq(rvT0, rvFA0, rvFA0)
		g.beqz(rvT0, slow)
		g.feq(rvT0, rvFA1, rvFA1)
		g.beqz(rvT0, slow)
		switch op {
		case "<":
			g.fop(0x51, 1, rvT0, rvFA0, rvFA1)
		case "<=":
			g.fop(0x51, 0, rvT0, rvFA0, rvFA1)
		case ">":
			g.fop(0x51, 1, rvT0, rvFA1, rvFA0)
		case ">=":
			g.fop(0x51, 0, rvT0, rvFA1, rvFA0)
		default:
			g.feq(rvT0, rvFA0, rvFA1)
			if op == "!=" {
				g.xori(rvT0, rvT0, 1)
			}
		}
		g.fcvtDL(rvFA0, rvT0)
		g.j(done)
	}
	g.bind(slow)
	g.numCall(numBinops[op])
	g.bind(done)
}

func (g *rvGen) unary(op string, operand Expression) error {
	switch op {
	case "-":
		if err := g.expr(operand); err != nil {
			return err
		}
		g.fmvD(rvFA1, rvFA0)
		g.fmvDX(rvFA0, 0)
		g.numBinop("-")
	case "not":
		if err := g.truthy(operand); err != nil {
			return err
		}
		g.w(rvI(1, rvA0, 3, rvA0, 0x13)) // sltiu a0, a0, 1
		g.fcvtDL(rvFA0, rvA0)
	case "#":
		if t := g.exprType(operand); t != "string" && t != "list" {
			return fmt.Errorf("riscv64: # is only supported on strings and lists")
		}
		if err := g.expr(operand); err != nil {
			return err
		}
		g.fmvXD(rvT0, rvFA0)
		g.fld(rvFA0, rvT0, 0)
	case "~b":
		if err := g.expr(operand); err != nil {
			return err
		}
		g.toI64()
		g.xori(rvA0, rvA0, -1)
		g.fromI64()
	default:
		return fmt.Errorf("riscv64: unary %s is not supported yet", op)
	}
	return nil
}

func (g *rvGen) bitwise(e *BinaryExpr) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	t := g.tmp()
	if err := g.expr(e.Left); err != nil {
		return err
	}
	g.toI64()
	g.sd(rvA0, rvS0, t)
	if err := g.expr(e.Right); err != nil {
		return err
	}
	g.toI64()
	g.mv(rvA1, rvA0)
	g.ld(rvA0, rvS0, t)
	switch e.Operator {
	case "|b":
		g.op(0, 6, rvA0, rvA0, rvA1)
	case "&b":
		g.op(0, 7, rvA0, rvA0, rvA1)
	case "^b":
		g.op(0, 4, rvA0, rvA0, rvA1)
	case "<<b":
		g.op(0, 1, rvA0, rvA0, rvA1)
	case "?b":
		g.op(0, 5, rvA0, rvA0, rvA1)
		g.andi(rvA0, rvA0, 1)
	case ">>b":
		g.op(0, 5, rvA0, rvA0, rvA1)
	case "<<<b", ">>>b":
		g.op(0x20, 0, rvA2, 0, rvA1)
		if e.Operator == "<<<b" {
			g.op(0, 1, rvT0, rvA0, rvA1)
			g.op(0, 5, rvT1, rvA0, rvA2)
		} else {
			g.op(0, 5, rvT0, rvA0, rvA1)
			g.op(0, 1, rvT1, rvA0, rvA2)
		}
		g.op(0, 6, rvA0, rvT0, rvT1)
	default:
		return fmt.Errorf("riscv64: operator %s is not supported yet", e.Operator)
	}
	g.fromI64()
	return nil
}

// truthyFA0 sets a0 to 1 when fa0 is truthy: non-zero numbers and boxed
// exact numbers are, zero and error NaNs are not.
func (g *rvGen) truthyFA0() {
	nan, done := g.newLabel(), g.newLabel()
	g.feq(rvT0, rvFA0, rvFA0)
	g.beqz(rvT0, nan)
	g.fmvDX(rvFT0, 0)
	g.feq(rvA0, rvFA0, rvFT0)
	g.xori(rvA0, rvA0, 1)
	g.j(done)
	g.bind(nan)
	g.fmvXD(rvA0, rvFA0)
	g.srli(rvA0, rvA0, 63)
	g.bind(done)
}

func (g *rvGen) truthy(e Expression) error {
	if err := g.expr(e); err != nil {
		return err
	}
	g.truthyFA0()
	return nil
}

// toI64 truncates fa0 to a signed 64-bit integer in a0.
func (g *rvGen) toI64() {
	inline, done := g.newLabel(), g.newLabel()
	g.feq(rvT0, rvFA0, rvFA0)
	g.bnez(rvT0, inline)
	g.numCall(numOpToI64)
	g.fmvXD(rvA0, rvFA0)
	g.j(done)
	g.bind(inline)
	g.fcvtLD(rvA0, rvFA0, 1)
	g.bind(done)
}

// fromI64 converts the signed integer in a0 to a number in fa0.
func (g *rvGen) fromI64() {
	slow, done := g.newLabel(), g.newLabel()
	g.li(rvT0, numFixMax-1)
	g.op(0, 0, rvT0, rvT0, rvA0)
	g.li(rvT1, 2*numFixMax-2)
	g.br(6, rvT1, rvT0, slow)
	g.fcvtDL(rvFA0, rvA0)
	g.j(done)
	g.bind(slow)
	g.fmvDX(rvFA0, rvA0)
	g.numCall(numOpFromI64)
	g.bind(done)
}

func (g *rvGen) toFloat() {
	done := g.newLabel()
	g.feq(rvT0, rvFA0, rvFA0)
	g.bnez(rvT0, done)
	g.numCall(numOpFloat)
	g.bind(done)
}

func (g *rvGen) fstring(e *FStringExpr) error {
	if len(e.Parts) == 0 {
		return g.expr(&StringExpr{})
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	acc := g.tmp()
	for i, p := range e.Parts {
		if g.exprType(p) == "list" {
			return fmt.Errorf("riscv64: lists cannot be interpolated into f-strings yet")
		}
		if err := g.expr(p); err != nil {
			return err
		}
		if g.exprType(p) != "string" {
			g.numCall(numOpStr)
		}
		if i > 0 {
			g.fmvXD(rvA1, rvFA0)
			g.ld(rvA0, rvS0, acc)
			g.call(g.l["strcat"])
			g.fmvDX(rvFA0, rvA0)
		}
		g.fsd(rvFA0, rvS0, acc)
	}
	return nil
}

// Calls.

func (g *rvGen) callExpr(c *CallExpr) error {
	name := c.Function
	if name == "me" && g.sc.fn != nil {
		name = g.sc.fn.name
	}
	if f := g.funcs[name]; f != nil {
		return g.userCall(f, c)
	}
	switch name {
	case "println", "print":
		for i, a := range c.Args {
			if i > 0 {
				g.printRaw(" ")
			}
			if err := g.printValue(a); err != nil {
				return err
			}
		}
		if name == "println" {
			g.printRaw("\n")
		}
		g.number(0)
		return nil
	case "printf":
		return g.printf(c)
	case "exit":
		if len(c.Args) > 0 {
			if err := g.expr(c.Args[0]); err != nil {
				return err
			}
			g.toI64()
		} else {
			g.li(rvA0, 0)
		}
		g.mv(rvS2, rvA0)
		g.call(g.l["flush"])
		g.mv(rvA0, rvS2)
		g.li(rvA7, 93)
		g.ecall()
		return nil
	case "str":
		if len(c.Args) == 1 {
			return g.expr(&CastExpr{Expr: c.Args[0], Type: "string"})
		}
	case "pow":
		if len(c.Args) == 2 {
			return g.expr(&BinaryExpr{Left: c.Args[0], Operator: "**", Right: c.Args[1]})
		}
	case "min", "max":
		if len(c.Args) == 2 {
			return g.minMax(name, c.Args[0], c.Args[1])
		}
	case "sqrt":
		if len(c.Args) == 1 {
			if err := g.expr(c.Args[0]); err != nil {
				return err
			}
			g.toFloat()
			g.fop(0x2D, 7, rvFA0, rvFA0, 0)
			return nil
		}
	case "floor", "ceil", "round", "abs", "trunc":
		if len(c.Args) == 1 {
			if err := g.expr(c.Args[0]); err != nil {
				return err
			}
			op, ok := numRoundBuiltins[name]
			if !ok {
				op = numOpTrunc
			}
			g.numCall(op)
			return nil
		}
	case "float":
		if len(c.Args) == 1 {
			if err := g.expr(c.Args[0]); err != nil {
				return err
			}
			g.toFloat()
			return nil
		}
	}
	return fmt.Errorf("riscv64: function %s is not supported yet", c.Function)
}

func (g *rvGen) userCall(f *rvFunc, c *CallExpr) error {
	if len(c.Args) != len(f.params) {
		return fmt.Errorf("riscv64: %s takes %d arguments, got %d", f.name, len(f.params), len(c.Args))
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	slots := make([]int32, len(c.Args))
	for i, a := range c.Args {
		slots[i] = g.tmp()
		if err := g.expr(a); err != nil {
			return err
		}
		g.fsd(rvFA0, rvS0, slots[i])
		if g.exprType(a) == "string" {
			f.paramTypes[i] = "string"
		}
	}
	if g.sc.fn == f && g.sc.tailExpr == Expression(c) {
		for i, p := range f.params {
			g.fld(rvFT0, rvS0, slots[i])
			g.fsd(rvFT0, rvS0, g.sc.vars[p])
		}
		g.j(g.sc.bodyStart)
		return nil
	}
	for i := range c.Args {
		g.fld(uint32(rvFA0+i), rvS0, slots[i])
	}
	g.call(f.label)
	return nil
}

func (g *rvGen) minMax(name string, a, b Expression) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	ta, tb := g.tmp(), g.tmp()
	if err := g.expr(a); err != nil {
		return err
	}
	g.fsd(rvFA0, rvS0, ta)
	if err := g.expr(b); err != nil {
		return err
	}
	g.fsd(rvFA0, rvS0, tb)
	g.fmvD(rvFA1, rvFA0)
	g.fld(rvFA0, rvS0, ta)
	if name == "min" {
		g.numBinop("<=")
	} else {
		g.numBinop(">=")
	}
	pickB, done := g.newLabel(), g.newLabel()
	g.fmvXD(rvT0, rvFA0)
	g.beqz(rvT0, pickB)
	g.fld(rvFA0, rvS0, ta)
	g.j(done)
	g.bind(pickB)
	g.fld(rvFA0, rvS0, tb)
	g.bind(done)
	return nil
}

func (g *rvGen) printRaw(s string) {
	if s == "" {
		return
	}
	g.la(rvA0, g.constData([]byte(s)))
	g.li(rvA1, int64(len(s)))
	g.call(g.l["print_raw"])
}

func (g *rvGen) printValue(e Expression) error {
	if s, ok := e.(*StringExpr); ok {
		g.printRaw(processEscapeSequences(s.Value))
		return nil
	}
	if err := g.expr(e); err != nil {
		return err
	}
	g.fmvXD(rvA0, rvFA0)
	switch g.exprType(e) {
	case "string":
		g.call(g.l["print_tstr"])
	case "list":
		g.call(g.l["print_list"])
	default:
		g.numCall(numOpStr)
		g.fmvXD(rvA0, rvFA0)
		g.call(g.l["print_tstr"])
	}
	return nil
}

func (g *rvGen) printf(c *CallExpr) error {
	if len(c.Args) == 0 {
		return fmt.Errorf("printf requires a format string")
	}
	f, ok := c.Args[0].(*StringExpr)
	if !ok {
		return fmt.Errorf("printf format must be a string literal")
	}
	format := processEscapeSequences(f.Value)
	arg := 1
	var lit strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			lit.WriteByte(format[i])
			continue
		}
		j := i + 1
		for j < len(format) && strings.IndexByte("-+ #0123456789", format[j]) >= 0 {
			j++
		}
		prec := 6
		if j < len(format) && format[j] == '.' {
			k := j + 1
			for k < len(format) && format[k] >= '0' && format[k] <= '9' {
				k++
			}
			if p, err := strconv.Atoi(format[j+1 : k]); err == nil {
				prec = min(p, 15)
			}
			j = k
		}
		for j < len(format) && strings.IndexByte("lhLjzt", format[j]) >= 0 {
			j++
		}
		if j >= len(format) {
			lit.WriteString(format[i:])
			break
		}
		conv := format[j]
		if conv == '%' {
			lit.WriteByte('%')
			i = j
			continue
		}
		if arg >= len(c.Args) {
			return fmt.Errorf("printf: not enough arguments for format string")
		}
		g.printRaw(lit.String())
		lit.Reset()
		a := c.Args[arg]
		arg++
		switch conv {
		case 'v', 's':
			if err := g.printValue(a); err != nil {
				return err
			}
		case 'd', 'i', 'u':
			if err := g.expr(a); err != nil {
				return err
			}
			g.toI64()
			g.call(g.l["print_i64"])
		case 'f', 'F', 'g', 'G', 'e', 'E':
			if err := g.expr(a); err != nil {
				return err
			}
			g.toFloat()
			g.li(rvA0, int64(prec))
			g.call(g.l["print_fixed"])
		default:
			return fmt.Errorf("riscv64: printf %%%c is not supported yet", conv)
		}
		i = j
	}
	g.printRaw(lit.String())
	g.number(0)
	return nil
}

// Lists are flat: [count][elem0][elem1]..., referenced by their raw address bits.

func (g *rvGen) list(e *ListExpr) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	p := g.tmp()
	g.li(rvA1, int64(8+8*len(e.Elements)))
	g.call(g.l["alloc"])
	g.sd(rvA0, rvS0, p)
	g.number(float64(len(e.Elements)))
	g.fsd(rvFA0, rvA0, 0)
	for i, el := range e.Elements {
		if err := g.expr(el); err != nil {
			return err
		}
		g.ld(rvT0, rvS0, p)
		g.li(rvT1, int64(8+8*i))
		g.op(0, 0, rvT0, rvT0, rvT1)
		g.fsd(rvFA0, rvT0, 0)
	}
	g.ld(rvT0, rvS0, p)
	g.fmvDX(rvFA0, rvT0)
	return nil
}

// elemAddr leaves the address of list element a0 in t0, or branches to oob.
func (g *rvGen) elemAddr(list uint32, oob *rvLabel) {
	g.fld(rvFT0, list, 0)
	g.fcvtLD(rvT1, rvFT0, 1)
	g.br(7, rvA0, rvT1, oob)
	g.slli(rvT0, rvA0, 3)
	g.op(0, 0, rvT0, rvT0, list)
	g.addi(rvT0, rvT0, 8)
}

func (g *rvGen) index(e *IndexExpr) error {
	if g.exprType(e.List) != "list" {
		return fmt.Errorf("riscv64: indexing is only supported on lists")
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	p := g.tmp()
	if err := g.expr(e.List); err != nil {
		return err
	}
	g.fsd(rvFA0, rvS0, p)
	if err := g.expr(e.Index); err != nil {
		return err
	}
	g.toI64()
	g.ld(rvT2, rvS0, p)
	oob, done := g.newLabel(), g.newLabel()
	g.elemAddr(rvT2, oob)
	g.fld(rvFA0, rvT0, 0)
	g.j(done)
	g.bind(oob)
	g.number(0)
	g.bind(done)
	return nil
}

func (g *rvGen) listUpdate(s *MapUpdateStmt) error {
	if g.varType(s.MapName) != "list" {
		return fmt.Errorf("riscv64: indexed update is only supported on lists")
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	i := g.tmp()
	if err := g.expr(s.Index); err != nil {
		return err
	}
	g.toI64()
	g.sd(rvA0, rvS0, i)
	if err := g.expr(s.Value); err != nil {
		return err
	}
	base, off, err := g.varRef(s.MapName)
	if err != nil {
		return err
	}
	g.ld(rvT2, base, off)
	g.ld(rvA0, rvS0, i)
	skip := g.newLabel()
	g.elemAddr(rvT2, skip)
	g.fsd(rvFA0, rvT0, 0)
	g.bind(skip)
	return nil
}

func (g *rvGen) listLoop(s *LoopStmt) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	p, i := g.tmp(), g.tmp()
	if err := g.expr(s.Iterable); err != nil {
		return err
	}
	g.fsd(rvFA0, rvS0, p)
	g.sd(0, rvS0, i)
	base, off, err := g.varRef(s.Iterator)
	if err != nil {
		return err
	}
	g.setVarType(s.Iterator, "")
	top, lp := g.newLabel(), rvLoop{g.newLabel(), g.newLabel()}
	g.bind(top)
	g.ld(rvT2, rvS0, p)
	g.ld(rvA0, rvS0, i)
	g.elemAddr(rvT2, lp.brk)
	g.fld(rvFA0, rvT0, 0)
	g.fsd(rvFA0, base, off)
	g.sc.loops = append(g.sc.loops, lp)
	if err := g.stmts(s.Body); err != nil {
		return err
	}
	g.sc.loops = g.sc.loops[:len(g.sc.loops)-1]
	g.bind(lp.cont)
	g.ld(rvT0, rvS0, i)
	g.addi(rvT0, rvT0, 1)
	g.sd(rvT0, rvS0, i)
	g.j(top)
	g.bind(lp.brk)
	return nil
}
