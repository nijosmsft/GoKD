package gokd

// bugCheckNames maps the most common Windows kernel bug-check codes to a
// (name, description) pair, derived from the public Microsoft bug-check
// reference. The full list is enormous (>200 codes), so we ship just the
// ~20 most frequently encountered; unknown codes return ("", "") and the
// caller still has the raw Code + Args from the underlying record.
var bugCheckNames = map[uint32]struct{ Name, Description string }{
	0x0A:  {"IRQL_NOT_LESS_OR_EQUAL", "Kernel-mode driver attempted to access pageable memory at too high an IRQL."},
	0x18:  {"REFERENCE_BY_POINTER", "Reference count of an object is wrong."},
	0x1A:  {"MEMORY_MANAGEMENT", "Memory manager detected a violation."},
	0x1E:  {"KMODE_EXCEPTION_NOT_HANDLED", "Kernel-mode exception was not handled."},
	0x3B:  {"SYSTEM_SERVICE_EXCEPTION", "Exception while executing a routine that transitions from non-privileged to privileged code."},
	0x4E:  {"PFN_LIST_CORRUPT", "Page frame number list is corrupt."},
	0x50:  {"PAGE_FAULT_IN_NONPAGED_AREA", "Invalid system memory referenced."},
	0x7E:  {"SYSTEM_THREAD_EXCEPTION_NOT_HANDLED", "A system thread generated an exception that the error handler did not catch."},
	0x7F:  {"UNEXPECTED_KERNEL_MODE_TRAP", "CPU generated a trap that the kernel was not allowed to catch."},
	0x9F:  {"DRIVER_POWER_STATE_FAILURE", "A driver is in an inconsistent or invalid power state."},
	0xC2:  {"BAD_POOL_CALLER", "A kernel-mode caller made a bad pool request."},
	0xC4:  {"DRIVER_VERIFIER_DETECTED_VIOLATION", "Driver Verifier caught a violation."},
	0xD1:  {"DRIVER_IRQL_NOT_LESS_OR_EQUAL", "A driver attempted to access pageable memory at too high an IRQL."},
	0xEF:  {"CRITICAL_PROCESS_DIED", "A critical system process died."},
	0xF4:  {"CRITICAL_OBJECT_TERMINATION", "A critical object was terminated unexpectedly."},
	0x101: {"CLOCK_WATCHDOG_TIMEOUT", "Clock interrupt was not received on a secondary processor."},
	0x124: {"WHEA_UNCORRECTABLE_ERROR", "Fatal hardware error reported by WHEA."},
	0x139: {"KERNEL_SECURITY_CHECK_FAILURE", "Kernel detected a corruption of a critical data structure."},
	0x153: {"KERNEL_LOCK_ENTRY_LEAKED_ON_THREAD_TERMINATION", "Thread terminated while holding a kernel lock."},
	0x1A5: {"KERNEL_AUTO_BOOST_INVALID_LOCK_RELEASE", "A lock was released incorrectly by an auto-boosted thread."},
}

// LookupBugCheckName returns the canonical name and a short description
// for a kernel bugcheck code, or two empty strings if the code is not in
// the embedded table. The caller still has the raw Code and Args.
func LookupBugCheckName(code uint32) (name, description string) {
	if e, ok := bugCheckNames[code]; ok {
		return e.Name, e.Description
	}
	return "", ""
}
