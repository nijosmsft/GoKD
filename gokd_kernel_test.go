package gokd_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nijosmsft/gokd"
)

// TestKernelAttachKDNET attaches to a live kernel target over KDNET.
//
// This test is skipped unless KDNET_CONN is set, e.g.:
//
//	KDNET_CONN="net:port=50000,key=W.X.Y.Z" go test -v -run TestKernelAttachKDNET .
//
// NOTE: do NOT include `target=...` in the connection string. The `target=`
// parameter is the "VM host machine name" for kdsrv indirection — using an
// IP there silently prevents dbgeng from opening the UDP listener and the
// subsequent WaitForEvent hangs forever. The correct form for a direct
// KDNET attach is just `net:port=N,key=W.X.Y.Z`.
//
// Per HANDOFF.md, this test MUST NOT be run on the local workstation — only
// on a remote lab node targeting a separate VM. Running a kernel debugger
// against the local machine (even with a remote-looking conn string) can
// hang the host.
func TestKernelAttachKDNET(t *testing.T) {
	connStr := os.Getenv("KDNET_CONN")
	if connStr == "" {
		t.Skip("KDNET_CONN not set; skipping kernel attach test. " +
			"Set e.g. KDNET_CONN=\"net:port=50000,key=W.X.Y.Z\" to run.")
	}

	sess, err := gokd.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer sess.Close()

	t.Logf("Connecting to kernel target: %s", connStr)
	t.Log("Waiting for target to respond (this may take time if the target needs to break in)...")

	// Give it 60 seconds to connect — kernel targets need time.
	// The target must be in debug mode and may need manual break-in.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err = sess.AttachKernel(ctx, connStr)
	if err != nil {
		t.Fatalf("AttachKernel failed: %v (target may be unreachable or not in debug mode)", err)
	}
	t.Log("Kernel attached!")

	// Modules
	mods, err := sess.Modules()
	if err != nil {
		t.Logf("Modules() failed: %v", err)
	} else {
		t.Logf("Found %d modules:", len(mods))
		for i, m := range mods {
			if i > 15 {
				t.Logf("  ... (%d more)", len(mods)-15)
				break
			}
			t.Logf("  %-30s base=0x%016x size=0x%x", m.Name, m.Base, m.Size)
		}
	}

	// Registers
	regs, err := sess.Registers()
	if err != nil {
		t.Logf("Registers() failed: %v", err)
	} else {
		for _, name := range []string{"rip", "rsp", "cr3"} {
			if r, ok := regs.ByName[name]; ok {
				t.Logf("  %s = 0x%x", r.Name, r.Value)
			}
		}
	}

	// Stack
	frames, err := sess.Stack()
	if err != nil {
		t.Logf("Stack() failed: %v", err)
	} else {
		t.Logf("Stack (%d frames):", len(frames))
		for i, f := range frames {
			if i > 10 {
				t.Logf("  ... (%d more)", len(frames)-10)
				break
			}
			sym := fmt.Sprintf("%s!%s+0x%x", f.Module, f.Function, f.Displacement)
			t.Logf("  #%d  0x%016x  %s", i, f.InstructionOffset, sym)
		}
	}

	// Threads
	threads, err := sess.Threads()
	if err != nil {
		t.Logf("Threads() failed: %v", err)
	} else {
		t.Logf("Threads: %d", len(threads))
		for i, th := range threads {
			if i > 5 {
				t.Logf("  ... (%d more)", len(threads)-5)
				break
			}
			t.Logf("  sys_id=%d handle=0x%x", th.SystemID, th.Handle)
		}
	}

	// Symbol resolution — try a kernel symbol
	for _, sym := range []string{"nt!KiSystemCall64Shadow", "nt!NtCreateFile", "nt!PsActiveProcessHead"} {
		addr, err := sess.NameToAddr(sym)
		if err != nil {
			t.Logf("  %s: %v", sym, err)
		} else {
			t.Logf("  %s = 0x%x", sym, addr)
		}
	}

	// Read kernel memory — try reading the kernel base (MZ header)
	if len(mods) > 0 {
		for _, m := range mods {
			if m.Name == "nt" || m.Name == "ntoskrnl" || m.Name == "ntkrnlmp" {
				data, err := sess.ReadMemory(m.Base, 2)
				if err != nil {
					t.Logf("ReadMemory(nt base) failed: %v", err)
				} else if len(data) >= 2 {
					t.Logf("  kernel @ 0x%x first 2 bytes: %c%c", m.Base, data[0], data[1])
				}
				break
			}
		}
	}

	t.Log("Kernel inspection complete")

	// Detach cleanly
	if err := sess.Detach(); err != nil {
		t.Logf("Detach: %v", err)
	}
}
