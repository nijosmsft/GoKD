// Output formatting helpers for the gokd CLI.
package main

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/nijosmsft/gokd"
)

func printStopEvent(ev *gokd.StopEvent) {
	if ev == nil {
		printf("stopped\n")
		return
	}
	tid := uint32(0)
	if ev.Thread != nil {
		tid = ev.Thread.SystemID
	}
	if ev.Exception != nil {
		printf("Stopped: %s at 0x%016x exception=0x%08x firstChance=%t thread=%d\n",
			ev.Reason, ev.Address, ev.Exception.Code, ev.Exception.FirstChance, tid)
		return
	}
	printf("Stopped: %s at 0x%016x thread=%d\n", ev.Reason, ev.Address, tid)
}

func printStack(frames []*gokd.Frame) {
	for i, f := range frames {
		sym := "?"
		if f.Module != "" || f.Function != "" {
			sym = fmt.Sprintf("%s!%s+0x%x", f.Module, f.Function, f.Displacement)
		}
		printf("#%-2d %016x  %s\n", i, f.InstructionOffset, sym)
	}
}

func printRegisters(regs *gokd.RegisterSet, names []string) {
	if len(names) == 0 {
		names = []string{"rax", "rbx", "rcx", "rdx", "rsi", "rdi", "r8", "r9", "r10", "r11", "r12", "r13", "r14", "r15", "rip", "rsp", "rbp", "eflags"}
	}
	var parts []string
	for _, name := range names {
		r, ok := regs.ByName[name]
		if !ok {
			r, ok = regs.ByName[strings.ToLower(name)]
		}
		if ok && r.Valid {
			parts = append(parts, fmt.Sprintf("%s=%016x", r.Name, r.Value))
		}
	}
	if len(parts) > 0 {
		printf("%s\n", strings.Join(parts, "  "))
	}
}

func printHexdump(addr uint64, data []byte, width int) {
	switch width {
	case 8:
		for off := 0; off < len(data); off += 16 {
			printf("%016x  ", addr+uint64(off))
			for i := 0; i < 2 && off+i*8+8 <= len(data); i++ {
				printf("%016x ", binary.LittleEndian.Uint64(data[off+i*8:]))
			}
			printf("\n")
		}
	case 4:
		for off := 0; off < len(data); off += 16 {
			printf("%016x  ", addr+uint64(off))
			for i := 0; i < 4 && off+i*4+4 <= len(data); i++ {
				printf("%08x ", binary.LittleEndian.Uint32(data[off+i*4:]))
			}
			printf("\n")
		}
	default:
		for off := 0; off < len(data); off += 16 {
			end := off + 16
			if end > len(data) {
				end = len(data)
			}
			printf("%016x  ", addr+uint64(off))
			for i := off; i < off+16; i++ {
				if i < end {
					printf("%02x ", data[i])
				} else {
					printf("   ")
				}
			}
			printf(" ")
			for _, b := range data[off:end] {
				if b >= 32 && b < 127 {
					printf("%c", b)
				} else {
					printf(".")
				}
			}
			printf("\n")
		}
	}
}
