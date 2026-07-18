package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnetSend(t *testing.T) {
	source := `println(enet.send(":65000", "hello"))
println(enet.send(":65000", "hi"))
`
	// UDP sendto succeeds even with no listener; the result is the byte count.
	testInlineTim(t, "enet_send", source, "5\n2\n")
}

func TestEnetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	recvSrc, recvExe := filepath.Join(dir, "recv.tim"), filepath.Join(dir, "recv")
	sendSrc, sendExe := filepath.Join(dir, "send.tim"), filepath.Join(dir, "send")

	if err := os.WriteFile(recvSrc, []byte(`for from, msg in ":65001" ! 1 {
	println(msg)
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sendSrc, []byte(`println(enet.send(":65001", "pong"))
`), 0o644); err != nil {
		t.Fatal(err)
	}

	platform := GetDefaultPlatform()
	if err := CompileTim(recvSrc, recvExe, platform); err != nil {
		t.Fatalf("receiver compilation failed: %v", err)
	}
	if err := CompileTim(sendSrc, sendExe, platform); err != nil {
		t.Fatalf("sender compilation failed: %v", err)
	}

	recv := exec.Command(recvExe)
	var recvOut strings.Builder
	recv.Stdout = &recvOut
	if err := recv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recv.Process.Kill() }()

	// Give the receiver a moment to bind before sending.
	time.Sleep(300 * time.Millisecond)
	if out, err := exec.Command(sendExe).CombinedOutput(); err != nil {
		t.Fatalf("send failed: %v\n%s", err, out)
	}

	done := make(chan error, 1)
	go func() { done <- recv.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = recv.Process.Kill()
		t.Fatal("receiver did not exit after its bound")
	}

	if got := recvOut.String(); !strings.Contains(got, "pong") {
		t.Errorf("receiver got %q, want it to contain \"pong\"", got)
	}
}
