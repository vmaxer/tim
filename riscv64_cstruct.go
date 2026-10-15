package main

import "fmt"

// C-compatible structs: a constructor call allocates the struct and stores
// each argument converted to its field's C type. Like C pointers, struct
// values are numeric addresses.

func (g *rvGen) cstruct(name string) *CStructDecl {
	if g.structs == nil {
		return nil
	}
	return g.structs[name]
}

// structOf names the cstruct type an expression evaluates to, if known.
func (g *rvGen) structOf(e Expression) string {
	switch e := e.(type) {
	case *CallExpr:
		if g.cstruct(e.Function) != nil {
			return e.Function
		}
		if f := g.funcs[e.Function]; f != nil {
			if g.typing[f.name] {
				return ""
			}
			g.typing[f.name] = true
			defer delete(g.typing, f.name)
			return g.structOf(f.body)
		}
	case *IdentExpr:
		if g.sc != nil {
			if t, ok := g.sc.structs[e.Name]; ok {
				return t
			}
		}
		return g.varStructs[e.Name]
	case *FieldAccessExpr:
		if f := g.structField(e); f != nil {
			return f.StructName
		}
	case *BlockExpr:
		if n := len(e.Statements); n > 0 {
			if s, ok := e.Statements[n-1].(*ExpressionStmt); ok {
				return g.structOf(s.Expr)
			}
		}
	case *MatchExpr:
		for _, c := range e.Clauses {
			if c.Result != nil {
				if t := g.structOf(c.Result); t != "" {
					return t
				}
			}
		}
		if e.DefaultExpr != nil {
			return g.structOf(e.DefaultExpr)
		}
	case *CastExpr:
		if g.cstruct(e.Type) != nil {
			return e.Type
		}
	}
	return ""
}

// inferStructVars records the cstruct type of every variable that is only
// ever bound to values of one cstruct type.
func (g *rvGen) inferStructVars(stmts []Statement) {
	if len(g.structs) == 0 {
		return
	}
	for changed := true; changed; {
		changed = false
		for _, s := range stmts {
			rvVisit(s, true, func(n any) {
				if a, ok := n.(*AssignStmt); ok {
					switch a.Value.(type) {
					case *MapExpr, *ListExpr, *StringExpr, *FStringExpr:
						g.containerVars[a.Name] = true
					}
					if t := g.structOf(a.Value); t != "" && g.varStructs[a.Name] == "" {
						g.varStructs[a.Name] = t
						changed = true
					}
				}
			})
		}
	}
}

// structField resolves obj.field to a cstruct field: by the object's known
// type, or by a field name that only one cstruct declares.
func (g *rvGen) structField(e *FieldAccessExpr) *CStructField {
	t := g.structOf(e.Object)
	if id, ok := e.Object.(*IdentExpr); ok && t == "" && g.containerVars[id.Name] {
		return nil
	}
	return g.lookupField(t, e.FieldName)
}

func (g *rvGen) lookupField(structName, field string) *CStructField {
	if d := g.cstruct(structName); d != nil {
		for i := range d.Fields {
			if d.Fields[i].Name == field {
				return &d.Fields[i]
			}
		}
		return nil
	}
	var found *CStructField
	for _, d := range g.structs {
		for i := range d.Fields {
			if d.Fields[i].Name == field {
				if found != nil {
					return nil
				}
				found = &d.Fields[i]
			}
		}
	}
	return found
}

func (g *rvGen) construct(d *CStructDecl, args []Expression) error {
	if len(args) != len(d.Fields) {
		return fmt.Errorf("riscv64: %s takes %d fields, got %d", d.Name, len(d.Fields), len(args))
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	p := g.tmp()
	g.li(rvA1, int64(max(d.Size, 8)))
	g.call(g.l["alloc"])
	g.sdx(rvA0, rvS0, p)
	for i, a := range args {
		if err := g.expr(a); err != nil {
			return err
		}
		g.storeField(&d.Fields[i], p)
	}
	g.ldx(rvT0, rvS0, p)
	g.fcvtDL(rvFA0, rvT0)
	return nil
}

// loadField reads field f of the struct at base into fa0.
func (g *rvGen) loadField(f *CStructField, base uint32) {
	off := int32(f.Offset)
	switch f.Type {
	case "float64", "double":
		g.fld(rvFA0, base, off)
	case "float32", "float":
		g.w(rvI(off, base, 2, rvFA0, 0x07))
		g.fop(0x21, 0, rvFA0, rvFA0, 0)
	case "uint64":
		g.ld(rvA0, base, off)
		g.fop(0x69, 7, rvFA0, rvA0, 3)
	case "ptr", "cstr", "pointer":
		g.ld(rvT0, base, off)
		g.fmvDX(rvFA0, rvT0)
	default:
		f3 := map[string]uint32{"int8": 0, "int16": 1, "int32": 2, "int64": 3, "uint8": 4, "uint16": 5, "uint32": 6}[f.Type]
		g.w(rvI(off, base, f3, rvA0, 0x03))
		g.fromI64()
	}
}

// storeField writes fa0 into field f of the struct whose pointer is in
// frame slot p, converting to the field's C type first.
func (g *rvGen) storeField(f *CStructField, p int32) {
	off := int32(f.Offset)
	switch f.Type {
	case "float64", "double":
		g.toFloat()
		g.ldx(rvT2, rvS0, p)
		g.fsd(rvFA0, rvT2, off)
	case "float32", "float":
		g.toFloat()
		g.fop(0x20, 7, rvFT0, rvFA0, 1)
		g.ldx(rvT2, rvS0, p)
		g.w(rvS(off, rvFT0, rvT2, 2, 0x27))
	case "ptr", "cstr", "pointer":
		g.fmvXD(rvT0, rvFA0)
		g.ldx(rvT2, rvS0, p)
		g.sd(rvT0, rvT2, off)
	default:
		g.toI64()
		g.ldx(rvT2, rvS0, p)
		f3 := map[string]uint32{"int8": 0, "uint8": 0, "int16": 1, "uint16": 1, "int32": 2, "uint32": 2}[f.Type]
		if f.Size == 8 {
			f3 = 3
		}
		g.w(rvS(off, rvA0, rvT2, f3, 0x23))
	}
}

func (g *rvGen) fieldLoad(e *FieldAccessExpr, f *CStructField) error {
	if err := g.expr(e.Object); err != nil {
		return err
	}
	g.toI64()
	g.mv(rvT2, rvA0)
	g.loadField(f, rvT2)
	return nil
}

func (g *rvGen) fieldStore(s *MapUpdateStmt, f *CStructField) error {
	if err := g.expr(s.Value); err != nil {
		return err
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	v, p := g.tmp(), g.tmp()
	g.fsdx(rvFA0, rvS0, v)
	if err := g.loadVar(rvFA0, s.MapName); err != nil {
		return err
	}
	g.toI64()
	g.sdx(rvA0, rvS0, p)
	g.fldx(rvFA0, rvS0, v)
	g.storeField(f, p)
	return nil
}

// rvMemField describes the element type of read_<kind>/write_<kind>.
func rvMemField(kind string) *CStructField {
	t, ok := map[string]string{
		"i8": "int8", "u8": "uint8", "i16": "int16", "u16": "uint16", "i32": "int32",
		"u32": "uint32", "i64": "int64", "u64": "uint64", "f32": "float32", "f64": "float64",
	}[kind]
	if !ok {
		return nil
	}
	return &CStructField{Type: t, Size: GetCTypeSize(t)}
}

// memAddr leaves ptr + index*size in a0; raw pointers are numeric values.
func (g *rvGen) memAddr(ptr, index Expression, size int) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	p := g.tmp()
	if err := g.expr(ptr); err != nil {
		return err
	}
	g.toI64()
	g.sdx(rvA0, rvS0, p)
	if err := g.expr(index); err != nil {
		return err
	}
	g.toI64()
	g.li(rvT0, int64(size))
	g.op(1, 0, rvA0, rvA0, rvT0)
	g.ldx(rvT0, rvS0, p)
	g.add(rvA0, rvA0, rvT0)
	return nil
}
