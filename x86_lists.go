package main

// printMultiBuiltin is print with several arguments: println's argument
// handling without the trailing newline.
const printMultiBuiltin = "\x00print"

// xmm0Expr is an internal operand whose value is already in xmm0, used to
// store a computed value through the regular assignment paths.
type xmm0Expr struct{}

func (*xmm0Expr) String() string  { return "<xmm0>" }
func (*xmm0Expr) expressionNode() {}

// compileAppendOwned lowers __append_owned(xs, owner, v...) (see
// listappend.go): append in place while xs is the buffer owner names and it
// has room, otherwise grow into a fresh owned buffer.
func (fc *TimCompiler) compileAppendOwned(call *CallExpr) {
	list := call.Args[0].(*IdentExpr).Name
	owner := call.Args[1].(*IdentExpr).Name
	store := func(name string) {
		fc.compileStatement(&AssignStmt{Name: name, Value: &xmm0Expr{}, Mutable: true, IsUpdate: true})
	}
	for _, v := range call.Args[2:] {
		fc.compileExpression(v)
		fc.out.SubImmFromReg("rsp", 16)
		fc.out.MovXmmToMem("xmm0", "rsp", 0)
		fc.compileExpression(&IdentExpr{Name: owner})
		fc.out.MovXmmToMem("xmm0", "rsp", 8)
		fc.compileExpression(&IdentExpr{Name: list})

		slow, room, done := fc.newNumLabel(), fc.newNumLabel(), fc.newNumLabel()
		fc.out.MovqXmmToReg("rax", "xmm0")
		fc.out.MovMemToReg("rcx", "rsp", 8)
		fc.out.CmpRegToReg("rax", "rcx")
		slow.jcc(JumpNotEqual)
		fc.out.TestRegReg("rax", "rax")
		slow.jcc(JumpEqual)
		fc.out.MovMemToXmm("xmm1", "rax", 8)
		fc.out.Cvttsd2si("rdx", "xmm1")
		fc.out.CmpRegToImm("rdx", 4)
		room.jcc(JumpLess)
		fc.out.LeaMemToReg("rcx", "rdx", -1)
		fc.out.TestRegReg("rcx", "rdx")
		slow.jcc(JumpEqual)
		room.bind()
		fc.out.MovRegToReg("rcx", "rdx")
		fc.out.ShlImmReg("rcx", 4)
		fc.out.AddRegToReg("rcx", "rax")
		fc.out.Cvtsi2sd("xmm1", "rdx")
		fc.out.MovXmmToMem("xmm1", "rcx", 16)
		fc.out.MovMemToXmm("xmm2", "rsp", 0)
		fc.out.MovXmmToMem("xmm2", "rcx", 24)
		fc.out.AddImmToReg("rdx", 1)
		fc.out.Cvtsi2sd("xmm1", "rdx")
		fc.out.MovXmmToMem("xmm1", "rax", 8)
		done.jmp()

		slow.bind()
		fc.out.MovMemToXmm("xmm1", "rsp", 0)
		fc.emitNumCall(valGrow)
		fc.out.MovXmmToMem("xmm0", "rsp", 0)
		store(owner)
		fc.out.MovMemToXmm("xmm0", "rsp", 0)
		store(list)
		done.bind()
		fc.out.AddImmToReg("rsp", 16)
	}
	fc.compileExpression(&IdentExpr{Name: list})
}
