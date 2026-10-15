package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Process, memory and ENet (UDP) builtins as raw Linux system calls.

const (
	rvSysOpenat   = 56
	rvSysClose    = 57
	rvSysLseek    = 62
	rvSysRead     = 63
	rvSysWrite    = 64
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
	case "read_file":
		if err = need(1); err == nil {
			err = g.readFile(args[0])
		}
		return true, err
	case "write_file":
		if err = need(2); err == nil {
			err = g.writeFile(args[0], args[1])
		}
		return true, err
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

// utf8Slot evaluates e into a frame slot holding its UTF-8 bytes: a length
// word followed by the NUL-terminated bytes.
func (g *rvGen) utf8Slot(e Expression) (int32, error) {
	slot := g.tmp()
	if err := g.expr(e); err != nil {
		return 0, err
	}
	g.numCall(valUTF8)
	g.fsdx(rvFA0, rvS0, slot)
	return slot, nil
}

func (g *rvGen) ioError() {
	g.li(rvT0, int64(errorValueBits("io")))
	g.fmvDX(rvFA0, rvT0)
}

// readFile reads the whole file at path into a string, or gives the "io"
// error.
func (g *rvGen) readFile(path Expression) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	p, err := g.utf8Slot(path)
	if err != nil {
		return err
	}
	fd, size, buf, got := g.tmp(), g.tmp(), g.tmp(), g.tmp()
	fail, failClose, loop, eof, done := g.newLabel(), g.newLabel(), g.newLabel(), g.newLabel(), g.newLabel()
	g.li(rvA0, -100)
	g.ldx(rvA1, rvS0, p)
	g.addi(rvA1, rvA1, 8)
	g.li(rvA2, 0)
	g.li(rvA3, 0)
	g.li(rvA7, rvSysOpenat)
	g.ecall()
	g.br(4, rvA0, 0, fail)
	g.sdx(rvA0, rvS0, fd)
	g.li(rvA1, 0)
	g.li(rvA2, 2)
	g.li(rvA7, rvSysLseek)
	g.ecall()
	g.br(4, rvA0, 0, failClose)
	g.sdx(rvA0, rvS0, size)
	g.ldx(rvA0, rvS0, fd)
	g.li(rvA1, 0)
	g.li(rvA2, 0)
	g.li(rvA7, rvSysLseek)
	g.ecall()
	g.ldx(rvA1, rvS0, size)
	g.addi(rvA1, rvA1, 1)
	g.call(g.l["alloc"])
	g.sdx(rvA0, rvS0, buf)
	g.sdx(0, rvS0, got)
	g.bind(loop)
	g.ldx(rvA2, rvS0, size)
	g.ldx(rvT0, rvS0, got)
	g.op(0x20, 0, rvA2, rvA2, rvT0)
	g.beqz(rvA2, eof)
	g.ldx(rvA1, rvS0, buf)
	g.add(rvA1, rvA1, rvT0)
	g.ldx(rvA0, rvS0, fd)
	g.li(rvA7, rvSysRead)
	g.ecall()
	g.br(5, 0, rvA0, eof)
	g.ldx(rvT0, rvS0, got)
	g.add(rvT0, rvT0, rvA0)
	g.sdx(rvT0, rvS0, got)
	g.j(loop)
	g.bind(eof)
	g.ldx(rvT0, rvS0, buf)
	g.ldx(rvT1, rvS0, got)
	g.add(rvT0, rvT0, rvT1)
	g.sb(0, rvT0, 0)
	g.ldx(rvA0, rvS0, fd)
	g.li(rvA7, rvSysClose)
	g.ecall()
	g.fldx(rvFA0, rvS0, buf)
	g.numCall(valFromUTF8)
	g.j(done)
	g.bind(failClose)
	g.ldx(rvA0, rvS0, fd)
	g.li(rvA7, rvSysClose)
	g.ecall()
	g.bind(fail)
	g.ioError()
	g.bind(done)
	return nil
}

// writeFile writes the UTF-8 bytes of content to the file at path,
// replacing it; the result is the number of bytes written or the "io" error.
func (g *rvGen) writeFile(path, content Expression) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	c, err := g.utf8Slot(content)
	if err != nil {
		return err
	}
	p, err := g.utf8Slot(path)
	if err != nil {
		return err
	}
	fd, put := g.tmp(), g.tmp()
	fail, failClose, loop, finished, done := g.newLabel(), g.newLabel(), g.newLabel(), g.newLabel(), g.newLabel()
	g.li(rvA0, -100)
	g.ldx(rvA1, rvS0, p)
	g.addi(rvA1, rvA1, 8)
	g.li(rvA2, linuxOpenWrite)
	g.li(rvA3, linuxFileMode)
	g.li(rvA7, rvSysOpenat)
	g.ecall()
	g.br(4, rvA0, 0, fail)
	g.sdx(rvA0, rvS0, fd)
	g.sdx(0, rvS0, put)
	g.bind(loop)
	g.ldx(rvA1, rvS0, c)
	g.ld(rvA2, rvA1, 0)
	g.ldx(rvT0, rvS0, put)
	g.op(0x20, 0, rvA2, rvA2, rvT0)
	g.beqz(rvA2, finished)
	g.addi(rvA1, rvA1, 8)
	g.add(rvA1, rvA1, rvT0)
	g.ldx(rvA0, rvS0, fd)
	g.li(rvA7, rvSysWrite)
	g.ecall()
	g.br(5, 0, rvA0, failClose)
	g.ldx(rvT0, rvS0, put)
	g.add(rvT0, rvT0, rvA0)
	g.sdx(rvT0, rvS0, put)
	g.j(loop)
	g.bind(finished)
	g.ldx(rvA0, rvS0, fd)
	g.li(rvA7, rvSysClose)
	g.ecall()
	g.ldx(rvA0, rvS0, put)
	g.fromI64()
	g.j(done)
	g.bind(failClose)
	g.ldx(rvA0, rvS0, fd)
	g.li(rvA7, rvSysClose)
	g.ecall()
	g.bind(fail)
	g.ioError()
	g.bind(done)
	return nil
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
