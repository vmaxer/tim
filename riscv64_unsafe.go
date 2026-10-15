package main

import (
	"fmt"
	"math"
	"strconv"
)

// Unsafe blocks: the riscv64 block of `unsafe { } { } { }` runs register
// moves, arithmetic, loads, stores and system calls directly. ra and s0-s11,
// which hold the frame, globals and loop counters, are saved on the stack
// around the block, so it may use any integer register as long as it pops
// what it pushes; t6 is the scratch register for immediates.

const rvUnsafeSave = 112

var rvABIRegs = map[string]uint32{
	"zero": 0, "ra": 1, "sp": 2, "gp": 3, "tp": 4, "t0": 5, "t1": 6, "t2": 7,
	"s0": 8, "fp": 8, "s1": 9, "t3": 28, "t4": 29, "t5": 30, "t6": 31,
}

func init() {
	for i := range uint32(8) {
		rvABIRegs["a"+strconv.Itoa(int(i))] = 10 + i
	}
	for i := uint32(2); i <= 11; i++ {
		rvABIRegs["s"+strconv.Itoa(int(i))] = 16 + i
	}
	for i := range uint32(32) {
		rvABIRegs["x"+strconv.Itoa(int(i))] = i
	}
}

func rvRegister(name string) (uint32, error) {
	if r, ok := rvABIRegs[resolveRegisterAlias(name, ArchRiscv64)]; ok {
		return r, nil
	}
	return 0, fmt.Errorf("riscv64: unknown register %q in unsafe block", name)
}

func (g *rvGen) unsafe(e *UnsafeExpr) error {
	var casts []*CastExpr
	syscalls := false
	for _, s := range e.RISCV64Block {
		switch s := s.(type) {
		case *SyscallStmt:
			syscalls = true
		case *RegisterAssignStmt:
			if c, ok := s.Value.(*CastExpr); ok {
				if _, isVar := c.Expr.(*IdentExpr); isVar {
					casts = append(casts, c)
				}
			}
		}
	}
	if syscalls {
		g.call(g.l["flush"])
	}
	save := int32(rvUnsafeSave + (8*len(casts)+15)&^15)
	g.addi(rvSP, rvSP, -save)
	g.sd(rvRA, rvSP, 0)
	for i := range uint32(12) {
		g.sd(rvSavedReg(i), rvSP, int32(8+8*i))
	}
	g.unsafeVals = map[*CastExpr]int32{}
	for i, c := range casts {
		if err := g.unsafeCast(rvA0, c); err != nil {
			return err
		}
		g.unsafeVals[c] = int32(rvUnsafeSave + 8*i)
		g.sd(rvA0, rvSP, g.unsafeVals[c])
	}
	g.unsafeDepth = 0
	for _, s := range e.RISCV64Block {
		if err := g.unsafeStmt(s); err != nil {
			return err
		}
	}
	if g.unsafeDepth != 0 {
		return fmt.Errorf("riscv64: unsafe block leaves %d values on the stack", g.unsafeDepth/8)
	}
	ret, asType := "a0", "uint64"
	if r := e.RISCV64Return; r != nil {
		ret, asType = r.Register, r.AsType
	}
	r, err := rvRegister(ret)
	if err != nil {
		return err
	}
	g.mv(rvT6, r)
	g.ld(rvRA, rvSP, 0)
	for i := range uint32(12) {
		g.ld(rvSavedReg(i), rvSP, int32(8+8*i))
	}
	g.addi(rvSP, rvSP, save)
	switch asType {
	case "float64":
		g.fmvDX(rvFA0, rvT6)
	case "int64", "int32", "int16", "int8", "uint64", "uint32", "uint16", "uint8", "pointer", "ptr", "cstr":
		g.mv(rvA0, rvT6)
		g.fromI64()
	default:
		return fmt.Errorf("riscv64: unsupported unsafe result type %s", asType)
	}
	return nil
}

func rvSavedReg(i uint32) uint32 {
	if i < 2 {
		return rvS0 + i
	}
	return rvS2 + i - 2
}

func (g *rvGen) unsafeStmt(s Statement) error {
	switch s := s.(type) {
	case *SyscallStmt:
		g.ecall()
	case *MemoryStore:
		return g.unsafeStore(s.Address, s.Offset, s.Size, s.Value)
	case *RegisterAssignStmt:
		if n := len(s.Register); n > 2 && s.Register[0] == '[' && s.Register[n-1] == ']' {
			return g.unsafeStore(s.Register[1:n-1], 0, "uint64", s.Value)
		}
		if s.Register == "stack" {
			src, ok := s.Value.(string)
			if !ok {
				return fmt.Errorf("riscv64: only a register can be pushed onto the stack")
			}
			r, err := rvRegister(src)
			if err != nil {
				return err
			}
			g.addi(rvSP, rvSP, -8)
			g.sd(r, rvSP, 0)
			g.unsafeDepth += 8
			return nil
		}
		rd, err := rvRegister(s.Register)
		if err != nil {
			return err
		}
		return g.unsafeAssign(rd, s.Value)
	default:
		return fmt.Errorf("riscv64: unsupported statement %T in unsafe block", s)
	}
	return nil
}

func (g *rvGen) unsafeAssign(rd uint32, v any) error {
	switch v := v.(type) {
	case *NumberExpr:
		g.li(rd, rvImmediate(v.Value))
	case string:
		if v == "stack" {
			g.ld(rd, rvSP, 0)
			g.addi(rvSP, rvSP, 8)
			g.unsafeDepth -= 8
			return nil
		}
		rs, err := rvRegister(v)
		if err != nil {
			return err
		}
		g.mv(rd, rs)
	case *RegisterOp:
		return g.unsafeOp(rd, v)
	case *MemoryLoad:
		return g.unsafeLoad(rd, v)
	case *CastExpr:
		if at, ok := g.unsafeVals[v]; ok {
			g.ld(rd, rvSP, at+g.unsafeDepth)
			return nil
		}
		return g.unsafeCast(rd, v)
	default:
		return fmt.Errorf("riscv64: unsupported value %T in unsafe block", v)
	}
	return nil
}

// rvImmediate is the register image of an unsafe-block literal: integers as
// themselves, fractions as their float64 bits.
func rvImmediate(v float64) int64 {
	if v != math.Trunc(v) {
		return int64(math.Float64bits(v))
	}
	return int64(v)
}

var rvUnsafeOps = map[string][2]uint32{
	"+": {0, 0}, "-": {0x20, 0}, "*": {1, 0}, "/": {1, 4}, "%": {1, 6},
	"&": {0, 7}, "|": {0, 6}, "^b": {0, 4}, "<<": {0, 1}, ">>": {0, 5},
}

func (g *rvGen) unsafeOp(rd uint32, op *RegisterOp) error {
	if op.Left == "" {
		rs, err := rvRegister(fmt.Sprint(op.Right))
		if err != nil {
			return err
		}
		g.xori(rd, rs, -1)
		return nil
	}
	f, ok := rvUnsafeOps[op.Operator]
	if !ok {
		return fmt.Errorf("riscv64: unsupported operator %s in unsafe block", op.Operator)
	}
	rs1, err := rvRegister(op.Left)
	if err != nil {
		return err
	}
	var rs2 uint32
	switch r := op.Right.(type) {
	case string:
		if rs2, err = rvRegister(r); err != nil {
			return err
		}
	case *NumberExpr:
		rs2 = rvT6
		g.li(rs2, rvImmediate(r.Value))
	default:
		return fmt.Errorf("riscv64: unsupported operand %T in unsafe block", r)
	}
	g.op(f[0], f[1], rd, rs1, rs2)
	return nil
}

var rvLoadF3 = map[string]uint32{
	"int8": 0, "int16": 1, "int32": 2, "int64": 3, "uint64": 3, "": 3,
	"uint8": 4, "uint16": 5, "uint32": 6,
}

var rvStoreF3 = map[string]uint32{
	"int8": 0, "uint8": 0, "int16": 1, "uint16": 1, "int32": 2, "uint32": 2,
	"int64": 3, "uint64": 3, "": 3,
}

func (g *rvGen) unsafeLoad(rd uint32, m *MemoryLoad) error {
	f3, ok := rvLoadF3[m.Size]
	if !ok {
		return fmt.Errorf("riscv64: unsupported load size %s", m.Size)
	}
	base, err := rvRegister(m.Address)
	if err != nil {
		return err
	}
	off := m.Offset
	if off < -2048 || off > 2047 {
		g.li(rd, off)
		g.add(rd, rd, base)
		base, off = rd, 0
	}
	g.w(rvI(int32(off), base, f3, rd, 0x03))
	return nil
}

func (g *rvGen) unsafeStore(addr string, off int64, size string, v any) error {
	f3, ok := rvStoreF3[size]
	if !ok {
		return fmt.Errorf("riscv64: unsupported store size %s", size)
	}
	base, err := rvRegister(addr)
	if err != nil {
		return err
	}
	if off < -2048 || off > 2047 {
		return fmt.Errorf("riscv64: store offset %d is out of range", off)
	}
	var src uint32
	switch v := v.(type) {
	case string:
		if src, err = rvRegister(v); err != nil {
			return err
		}
	case *NumberExpr:
		src = rvT6
		g.li(src, rvImmediate(v.Value))
	default:
		return fmt.Errorf("riscv64: unsupported stored value %T", v)
	}
	g.w(rvS(int32(off), src, base, f3, 0x23))
	return nil
}

// unsafeCast loads `n as T` or `variable as T`: a variable as cstr is its
// NUL-terminated bytes, any other variable its raw bits. Variables are read
// before the block runs, while the frame registers still hold the frame.
func (g *rvGen) unsafeCast(rd uint32, c *CastExpr) error {
	switch e := c.Expr.(type) {
	case *NumberExpr:
		g.li(rd, int64(e.Value))
	case *IdentExpr:
		if err := g.loadVar(rvFA0, e.Name); err != nil {
			return err
		}
		if c.Type == "cstr" || c.Type == "cstring" {
			g.cPointer()
			g.mv(rd, rvA0)
		} else {
			g.fmvXD(rd, rvFA0)
		}
	default:
		return fmt.Errorf("riscv64: unsupported cast %T in unsafe block", e)
	}
	return nil
}
