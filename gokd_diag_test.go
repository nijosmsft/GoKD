package gokd_test

import (
	"testing"

	"github.com/nijosmsft/gokd"
)

func TestCreateProcessAndInspect(t *testing.T) {
	sess, err := gokd.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer sess.Close()

	t.Log("Creating notepad.exe under debugger...")
	if err := sess.CreateProcess("notepad.exe", gokd.CreateOptions{
		Flags: 0, // let DbgEng handle the flags
	}); err != nil {
		t.Fatalf("CreateProcess failed: %v", err)
	}
	defer sess.Detach()

	t.Log("Process created, now reading modules...")
	mods, err := sess.Modules()
	if err != nil {
		t.Fatalf("Modules() failed: %v", err)
	}
	t.Logf("Found %d modules:", len(mods))
	for _, m := range mods {
		t.Logf("  %-20s base=0x%016x size=0x%x", m.Name, m.Base, m.Size)
	}

	t.Log("Reading registers...")
	regs, err := sess.Registers()
	if err != nil {
		t.Fatalf("Registers() failed: %v", err)
	}
	rip, ok := regs.ByName["rip"]
	if ok {
		t.Logf("rip = 0x%x", rip.Value)
	}

	t.Log("Reading stack...")
	frames, err := sess.Stack()
	if err != nil {
		t.Fatalf("Stack() failed: %v", err)
	}
	for i, f := range frames {
		if i > 10 {
			t.Logf("  ... (%d more)", len(frames)-10)
			break
		}
		t.Logf("  #%d  0x%016x  %s!%s+0x%x", i, f.InstructionOffset,
			f.Module, f.Function, f.Displacement)
	}

	t.Log("Reading threads...")
	threads, err := sess.Threads()
	if err != nil {
		t.Fatalf("Threads() failed: %v", err)
	}
	for _, th := range threads {
		t.Logf("  thread sys_id=%d handle=0x%x", th.SystemID, th.Handle)
	}

	t.Log("Symbol resolution...")
	addr, err := sess.NameToAddr("ntdll!RtlUserThreadStart")
	if err != nil {
		t.Logf("NameToAddr failed: %v (may need symbols)", err)
	} else {
		t.Logf("ntdll!RtlUserThreadStart = 0x%x", addr)
	}

	// Read MZ header from ntdll
	for _, m := range mods {
		if m.Name == "ntdll" {
			data, err := sess.ReadMemory(m.Base, 2)
			if err != nil {
				t.Logf("ReadMemory(ntdll base) failed: %v", err)
			} else {
				t.Logf("ntdll @ 0x%x first 2 bytes: %c%c", m.Base, data[0], data[1])
			}
			break
		}
	}

	t.Log("All inspections complete")
}
