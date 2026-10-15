package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Process, memory and ENet (UDP) builtins as raw Linux system calls.

const (
	rvSysClose    = 57
	rvSysExitGrp  = 94
	rvSysGetpid   = 172
	rvSysSocket   = 198
	rvSysBind     = 200
	rvSysSendto   = 206
	rvSysRecvfrom = 207
	rvSysMunmap   = 215
	rvSysClone    = 220
	rvSysMmap     = 222
	rvSysWait4    = 260
)

// syscallArgs evaluates args as integers into a0.. and makes system call nr,
// leaving the result in a0.
func (g *rvGen) syscallArgs(nr int64, args []Expression, adjust func()) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	slots := make([]int32, len(args))
	for i, a := range args {
		slots[i] = g.tmp()
		if err := g.expr(a); err != nil {
			return err
		}
		g.toI64()
		g.sdx(rvA0, rvS0, slots[i])
	}
	for i := range args {
		g.ldx(uint32(rvA0+i), rvS0, slots[i])
	}
	if adjust != nil {
		adjust()
	}
	g.li(rvA7, nr)
	g.ecall()
	return nil
}

func (g *rvGen) syscallBuiltin(c *CallExpr) (bool, error) {
	args := c.Args
	need := func(n int) error {
		if len(args) != n {
			return fmt.Errorf("riscv64: %s() takes %d arguments", c.Function, n)
		}
		return nil
	}
	var err error
	switch c.Function {
	case "getpid":
		if err = need(0); err == nil {
			err = g.syscallArgs(rvSysGetpid, nil, nil)
		}
	case "fork":
		if err = need(0); err == nil {
			g.call(g.l["flush"])
			err = g.syscallArgs(rvSysClone, nil, func() {
				g.li(rvA0, 17)
				for r := uint32(rvA1); r <= rvA4; r++ {
					g.li(r, 0)
				}
			})
		}
	case "waitpid":
		if err = need(3); err == nil {
			err = g.syscallArgs(rvSysWait4, args, func() { g.li(rvA3, 0) })
		}
	case "mmap":
		if err = need(6); err == nil {
			err = g.syscallArgs(rvSysMmap, args, func() {
				g.li(rvT0, 0x1000)
				g.op(0, 7, rvT1, rvA3, rvT0)
				g.op(0, 4, rvA3, rvA3, rvT1)
				g.srli(rvT1, rvT1, 7)
				g.op(0, 6, rvA3, rvA3, rvT1)
			})
		}
	case "munmap":
		if err = need(2); err == nil {
			err = g.syscallArgs(rvSysMunmap, args, nil)
		}
	case "proc_exit":
		if err = need(1); err == nil {
			g.call(g.l["flush"])
			err = g.syscallArgs(rvSysExitGrp, args, nil)
		}
	case "enet.send":
		if err = need(2); err == nil {
			err = g.enetSend(args[0], args[1])
		}
	default:
		return false, nil
	}
	if err != nil {
		return true, err
	}
	g.fromI64()
	return true, nil
}

// sockaddr writes a sockaddr_in for 127.0.0.1 (or INADDR_ANY when any) and
// the port in t2 into the 16 bytes at frame slot at.
func (g *rvGen) sockaddr(at int32, any bool) {
	g.slotAddr(rvT0, at)
	g.li(rvT1, 2)
	g.w(rvS(0, rvT1, rvT0, 1, 0x23))
	g.srli(rvT1, rvT2, 8)
	g.andi(rvT3, rvT2, 0xff)
	g.slli(rvT3, rvT3, 8)
	g.op(0, 6, rvT1, rvT1, rvT3)
	g.w(rvS(2, rvT1, rvT0, 1, 0x23))
	addr := int64(0x0100007F)
	if any {
		addr = 0
	}
	g.li(rvT1, addr)
	g.w(rvS(4, rvT1, rvT0, 2, 0x23))
	g.sd(0, rvT0, 8)
}

// portOf parses ":port" (or ":lo-hi") at compile time.
func portOf(e Expression) (lo, hi int64, err error) {
	s, ok := e.(*StringExpr)
	if !ok || !strings.HasPrefix(s.Value, ":") {
		return 0, 0, fmt.Errorf("riscv64: ENet addresses must be literal \":port\" strings")
	}
	from, to, isRange := strings.Cut(s.Value[1:], "-")
	lo, err = strconv.ParseInt(from, 10, 32)
	hi = lo
	if err == nil && isRange {
		hi, err = strconv.ParseInt(to, 10, 32)
	}
	if err != nil || lo < 1 || hi > 65535 || hi < lo {
		return 0, 0, fmt.Errorf("riscv64: invalid ENet address %q", s.Value)
	}
	return lo, hi, nil
}

// enetSend sends msg as one UDP datagram to localhost; the result is the
// number of bytes sent.
func (g *rvGen) enetSend(target, msg Expression) error {
	port, _, err := portOf(target)
	if err != nil {
		return err
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	buf, fd := g.tmp(), g.tmp()
	g.tmp()
	sa := g.tmp()
	if err := g.expr(msg); err != nil {
		return err
	}
	g.numCall(valUTF8)
	g.fsdx(rvFA0, rvS0, buf)
	g.li(rvA0, 2)
	g.li(rvA1, 2)
	g.li(rvA2, 0)
	g.li(rvA7, rvSysSocket)
	g.ecall()
	g.sdx(rvA0, rvS0, fd)
	g.li(rvT2, port)
	g.sockaddr(sa, false)
	g.ldx(rvA0, rvS0, fd)
	g.ldx(rvA1, rvS0, buf)
	g.ld(rvA2, rvA1, 0)
	g.addi(rvA1, rvA1, 8)
	g.li(rvA3, 0)
	g.slotAddr(rvA4, sa)
	g.li(rvA5, 16)
	g.li(rvA7, rvSysSendto)
	g.ecall()
	g.sdx(rvA0, rvS0, buf)
	g.ldx(rvA0, rvS0, fd)
	g.li(rvA7, rvSysClose)
	g.ecall()
	g.ldx(rvA0, rvS0, buf)
	return nil
}

// receiveLoop binds a UDP port (the first free one of a range) and runs the
// body for each datagram: the message as a string, the sender's port as a
// number.
func (g *rvGen) receiveLoop(s *ReceiveLoopStmt) error {
	lo, hi, err := portOf(s.Address)
	if err != nil {
		return err
	}
	const bufSize = 256
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	fd, cnt, alen := g.tmp(), g.tmp(), g.tmp()
	g.tmp()
	sa := g.tmp()
	g.tmps(bufSize/8 - 1)
	buf := g.tmp()
	g.li(rvA0, 2)
	g.li(rvA1, 2)
	g.li(rvA2, 0)
	g.li(rvA7, rvSysSocket)
	g.ecall()
	g.sdx(rvA0, rvS0, fd)
	g.li(rvT2, lo)
	g.sdx(rvT2, rvS0, cnt)
	try, bound, failed := g.newLabel(), g.newLabel(), g.newLabel()
	g.bind(try)
	g.ldx(rvT2, rvS0, cnt)
	g.li(rvT0, hi)
	g.br(4, rvT0, rvT2, failed)
	g.sockaddr(sa, true)
	g.ldx(rvA0, rvS0, fd)
	g.slotAddr(rvA1, sa)
	g.li(rvA2, 16)
	g.li(rvA7, rvSysBind)
	g.ecall()
	g.beqz(rvA0, bound)
	g.ldx(rvT2, rvS0, cnt)
	g.addi(rvT2, rvT2, 1)
	g.sdx(rvT2, rvS0, cnt)
	g.j(try)
	g.bind(failed)
	g.printRaw(fmt.Sprintf("enet: no free port in %d-%d\n", lo, hi))
	g.exit(1)
	g.bind(bound)
	g.sdx(0, rvS0, cnt)
	lp := rvLoop{g.newLabel(), g.newLabel()}
	g.bind(lp.cont)
	if s.MaxIterations > 0 && s.MaxIterations != math.MaxInt64 {
		g.ldx(rvT0, rvS0, cnt)
		g.li(rvT1, s.MaxIterations)
		g.br(5, rvT0, rvT1, lp.brk)
		g.addi(rvT0, rvT0, 1)
		g.sdx(rvT0, rvS0, cnt)
	}
	g.li(rvT0, 16)
	g.sdx(rvT0, rvS0, alen)
	g.call(g.l["flush"])
	g.ldx(rvA0, rvS0, fd)
	g.slotAddr(rvA1, buf)
	g.li(rvA2, bufSize-1)
	g.li(rvA3, 0)
	g.slotAddr(rvA4, sa)
	g.slotAddr(rvA5, alen)
	g.li(rvA7, rvSysRecvfrom)
	g.ecall()
	g.br(4, rvA0, 0, lp.brk)
	g.slotAddr(rvT0, buf)
	g.add(rvT0, rvT0, rvA0)
	g.sb(0, rvT0, 0)
	g.slotAddr(rvT0, buf)
	g.fmvDX(rvFA0, rvT0)
	g.numCall(valFromUTF8)
	if s.MessageVar != "" {
		if err := g.storeVar(rvFA0, s.MessageVar); err != nil {
			return err
		}
	}
	if s.SenderVar != "" {
		g.slotAddr(rvT0, sa)
		g.lbu(rvT1, rvT0, 2)
		g.lbu(rvT2, rvT0, 3)
		g.slli(rvT1, rvT1, 8)
		g.op(0, 6, rvA0, rvT1, rvT2)
		g.fcvtDL(rvFA0, rvA0)
		if err := g.storeVar(rvFA0, s.SenderVar); err != nil {
			return err
		}
	}
	if err := g.loopBody(lp, s.Body); err != nil {
		return err
	}
	g.j(lp.cont)
	g.bind(lp.brk)
	g.ldx(rvA0, rvS0, fd)
	g.li(rvA7, rvSysClose)
	g.ecall()
	return nil
}
