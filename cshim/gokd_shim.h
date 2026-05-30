/*
 * gokd_shim.h — GoKD C shim public API
 *
 * This is the ONLY header included by CGo. It exposes a flat C API over
 * the Windows DbgEng COM interfaces (IDebugClient6, IDebugControl6,
 * IDebugDataSpaces4, IDebugSymbols5, IDebugRegisters2,
 * IDebugSystemObjects4, IDebugAdvanced3) and DbgHelp.
 *
 * All COM state lives inside the shim. Callers hold an opaque uint64_t
 * session handle. No COM objects, C++ types, or wchar_t strings cross
 * this boundary.
 *
 * String encoding: all char* parameters are UTF-8. The shim converts
 * to/from UTF-16 internally before calling DbgEng/DbgHelp.
 *
 * Error convention: functions returning int32_t return an HRESULT.
 *   S_OK  (0)  = success
 *   < 0        = failure (standard HRESULT error code)
 * gokd_create_session() returns 0 on failure.
 */

#pragma once

#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* ====================================================================== */
/*  Opaque session handle                                                 */
/* ====================================================================== */

typedef uint64_t gokd_session_t; /* 0 = invalid */

/* ====================================================================== */
/*  Data structures                                                       */
/* ====================================================================== */

/* Stack frame */
typedef struct {
    uint64_t instruction_offset;
    uint64_t return_offset;
    uint64_t frame_offset;
    uint64_t stack_offset;
    char     module[256];
    char     function[512];
    uint64_t displacement;
    char     source_file[512];
    uint32_t source_line;
} gokd_frame_t;

/* Register */
typedef struct {
    char     name[64];
    uint64_t value;
    uint32_t type;   /* GOKD_REG_TYPE_* */
    uint8_t  valid;
} gokd_register_t;

#define GOKD_REG_TYPE_INT8      0
#define GOKD_REG_TYPE_INT16     1
#define GOKD_REG_TYPE_INT32     2
#define GOKD_REG_TYPE_INT64     3
#define GOKD_REG_TYPE_FLOAT32   4
#define GOKD_REG_TYPE_FLOAT64   5
#define GOKD_REG_TYPE_FLOAT80   6
#define GOKD_REG_TYPE_VECTOR128 7

/* Module */
typedef struct {
    char     name[256];       /* short name, no path */
    char     image_name[512]; /* full image path */
    uint64_t base;
    uint32_t size;
    uint32_t timestamp;
    uint32_t checksum;
    uint32_t symbol_type;     /* GOKD_SYMTYPE_* — DEBUG_MODULE_PARAMETERS::SymbolType */
} gokd_module_t;

/* Symbol type values mirror DEBUG_SYMTYPE_* in dbgeng.h. */
#define GOKD_SYMTYPE_NONE     0
#define GOKD_SYMTYPE_COFF     1
#define GOKD_SYMTYPE_CODEVIEW 2
#define GOKD_SYMTYPE_PDB      3
#define GOKD_SYMTYPE_EXPORT   4
#define GOKD_SYMTYPE_DEFERRED 5
#define GOKD_SYMTYPE_SYM      6
#define GOKD_SYMTYPE_DIA      7

/* Thread */
typedef struct {
    uint32_t system_id;
    uint64_t handle;
    uint64_t data_offset;
    uint64_t start_offset;
} gokd_thread_t;

/* Breakpoint */
typedef struct {
    uint32_t id;
    uint64_t offset;
    char     expression[512];
    uint32_t flags;   /* DEBUG_BREAKPOINT_* flags */
    uint32_t enabled;
} gokd_bp_t;

/* Type field */
typedef struct {
    char     name[256];
    uint32_t offset;      /* byte offset within parent struct */
    uint64_t size;        /* size in bytes */
    char     type_name[256];
} gokd_field_t;

/* ====================================================================== */
/*  Expression evaluation (t1-1)                                          */
/* ====================================================================== */

/* Value kinds mirror DEBUG_VALUE_* in dbgeng.h. */
#define GOKD_VALUE_INVALID   0
#define GOKD_VALUE_INT8      1
#define GOKD_VALUE_INT16     2
#define GOKD_VALUE_INT32     3
#define GOKD_VALUE_INT64     4
#define GOKD_VALUE_FLOAT32   5
#define GOKD_VALUE_FLOAT64   6
#define GOKD_VALUE_FLOAT80   7
#define GOKD_VALUE_FLOAT82   8
#define GOKD_VALUE_FLOAT128  9
#define GOKD_VALUE_VECTOR64  10
#define GOKD_VALUE_VECTOR128 11

#define GOKD_EXPR_MASM 0
#define GOKD_EXPR_CPP  1

/*
 * Evaluated expression value. type carries one of GOKD_VALUE_*; u64 is the
 * zero-extended integer slot for INT8/16/32/64; f64 carries the FLOAT32/64
 * value; raw[24] holds the full DEBUG_VALUE tail for callers that need the
 * float-80/82/128 or vector-64/128 payload (little-endian, padded to 24
 * bytes per dbgeng.h).
 */
typedef struct gokd_value_s {
    uint32_t type;
    uint64_t u64;
    double   f64;
    uint8_t  raw[24];
} gokd_value_t;

/* ====================================================================== */
/*  Event types                                                           */
/* ====================================================================== */

#define GOKD_EVENT_BREAKPOINT      1
#define GOKD_EVENT_EXCEPTION       2
#define GOKD_EVENT_THREAD_CREATED  3
#define GOKD_EVENT_THREAD_EXITED   4
#define GOKD_EVENT_PROC_CREATED    5
#define GOKD_EVENT_PROC_EXITED     6
#define GOKD_EVENT_MOD_LOADED      7
#define GOKD_EVENT_MOD_UNLOADED    8
#define GOKD_EVENT_SESSION_STATUS  9

/* SessionStatus codes mirror the DEBUG_SESSION_* constants in dbgeng.h. */
#define GOKD_SESSION_ACTIVE                       0
#define GOKD_SESSION_END_SESSION_ACTIVE_TERMINATE 1
#define GOKD_SESSION_END_SESSION_ACTIVE_DETACH    2
#define GOKD_SESSION_END_SESSION_PASSIVE          3
#define GOKD_SESSION_END                          4
#define GOKD_SESSION_REBOOT                       5
#define GOKD_SESSION_HIBERNATE                    6
#define GOKD_SESSION_FAILURE                      7

/* Stop reasons (returned by gokd_go/step_in/step_over/step_out) */
#define GOKD_STOP_BREAKPOINT    1
#define GOKD_STOP_STEP          2
#define GOKD_STOP_EXCEPTION     3
#define GOKD_STOP_PROC_EXIT     4

/* Stop event — returned by execution commands */
typedef struct {
    uint32_t reason;          /* GOKD_STOP_* */
    uint64_t address;
    uint32_t thread_sys_id;
    /* Exception details (valid when reason == GOKD_STOP_EXCEPTION) */
    uint32_t exception_code;
    uint32_t exception_first_chance;
} gokd_stop_event_t;

/* Event data structs — passed as const void* to gokd_event_fn */

typedef struct {
    uint32_t bp_id;
    uint64_t address;
    uint32_t thread_sys_id;
} gokd_ev_breakpoint_t;

typedef struct {
    uint32_t code;
    uint64_t address;
    uint32_t first_chance;
    uint32_t thread_sys_id;
} gokd_ev_exception_t;

typedef struct {
    uint32_t sys_id;
    uint64_t handle;
    uint64_t data_offset;
    uint64_t start_offset;
} gokd_ev_thread_created_t;

typedef struct {
    uint32_t sys_id;
    uint32_t exit_code;
} gokd_ev_thread_exited_t;

typedef struct {
    uint64_t base_offset;
    uint32_t module_size;
    char     module_name[256];
    char     image_name[512];
} gokd_ev_proc_created_t;

typedef struct {
    uint32_t exit_code;
} gokd_ev_proc_exited_t;

typedef struct {
    uint64_t base_offset;
    uint32_t module_size;
    char     module_name[256];
    char     image_name[512];
} gokd_ev_mod_loaded_t;

typedef struct {
    uint64_t base_offset;
    char     image_base_name[256];
} gokd_ev_mod_unloaded_t;

typedef struct {
    uint32_t status; /* GOKD_SESSION_* */
} gokd_ev_session_status_t;

/* ====================================================================== */
/*  Callbacks                                                             */
/* ====================================================================== */

/*
 * Event callback: fired from the dispatch thread during WaitForEvent.
 * event_type is one of GOKD_EVENT_*.
 * event_data points to the matching gokd_ev_*_t struct (valid for the
 * duration of the call only).
 */
typedef void (*gokd_event_fn)(gokd_session_t s, int event_type,
                               const void *event_data, void *ctx);

/* Output callback: fired from the dispatch thread. */
typedef void (*gokd_output_fn)(gokd_session_t s, uint32_t mask,
                                const char *text, void *ctx);

/* ====================================================================== */
/*  Session lifecycle                                                     */
/* ====================================================================== */

/*
 * Create a new debug session. Returns 0 on failure.
 * Internally calls CoInitializeEx(MTA) and DebugCreate on the calling
 * thread — the caller MUST ensure this runs on the dedicated dispatch
 * thread.
 */
gokd_session_t gokd_create_session(void);

/*
 * Destroy a session and release all COM interfaces.
 * Must be called from the dispatch thread.
 */
void gokd_destroy_session(gokd_session_t s);

/* Return the HRESULT from the most recent failed call. */
int32_t gokd_get_last_error(gokd_session_t s);

/* ====================================================================== */
/*  Attach modes                                                          */
/* ====================================================================== */

/*
 * Attach to a running process.
 * flags: 0 = default (invasive), or DEBUG_ATTACH_NONINVASIVE, etc.
 * Calls WaitForEvent internally to wait for the initial break.
 */
int32_t gokd_attach_process(gokd_session_t s, uint32_t pid, uint32_t flags);

/*
 * Launch a new process under the debugger.
 * flags: DEBUG_PROCESS | DEBUG_ONLY_THIS_PROCESS, etc.
 * initial_break: nonzero = stop at the initial break-in (default
 *   DbgEng behaviour). Zero = resume the target immediately after the
 *   initial break, so the caller does not have to issue Go() to start
 *   execution.
 */
int32_t gokd_create_process(gokd_session_t s, const char *cmd,
                             uint32_t flags, int initial_break);

/*
 * Attach to a kernel target.
 * options: "net:port=50000,key=..." or "com:port=\\\\.\\COM1,baud=115200"
 * flags:   bitmask of GOKD_KERNEL_* values.
 *
 * When GOKD_KERNEL_INITIAL_BREAK_IN is set (recommended for programmatic
 * use), the shim issues SetInterrupt(DEBUG_INTERRUPT_ACTIVE) immediately
 * after the transport opens. This makes the engine push a break-in
 * request to the target as soon as the connection handshakes, giving
 * deterministic first-break behaviour (kd.exe waits passively because
 * it's interactive — a library has no console to Ctrl+Break).
 */
#define GOKD_KERNEL_INITIAL_BREAK_IN 0x00000001
int32_t gokd_attach_kernel(gokd_session_t s, const char *options,
                            uint32_t flags);

/* Open a crash dump or minidump file for offline analysis. */
int32_t gokd_open_dump(gokd_session_t s, const char *path);

/* Detach from the current target. */
int32_t gokd_detach(gokd_session_t s);

/* ====================================================================== */
/*  Remote debugging                                                      */
/* ====================================================================== */

/*
 * Connect to a remote process server (dbgsrv.exe).
 * connection: "tcp:server=192.168.1.10,port=5005" etc.
 * After connecting, AttachProcess/CreateProcess will target the remote.
 */
int32_t gokd_connect_remote(gokd_session_t s, const char *connection);

/* Disconnect from the remote process server. */
int32_t gokd_disconnect_remote(gokd_session_t s);

/* ====================================================================== */
/*  Execution control                                                     */
/* ====================================================================== */

/*
 * Resume execution and wait for a stop event.
 * Blocks until the target breaks (breakpoint, exception, exit).
 * Fills *out with the stop reason and context.
 */
int32_t gokd_go(gokd_session_t s, gokd_stop_event_t *out);
int32_t gokd_step_in(gokd_session_t s, gokd_stop_event_t *out);
int32_t gokd_step_over(gokd_session_t s, gokd_stop_event_t *out);
int32_t gokd_step_out(gokd_session_t s, gokd_stop_event_t *out);

/*
 * Asynchronous break-in. Safe to call from ANY thread.
 * Causes WaitForEvent to return on the dispatch thread.
 */
int32_t gokd_break_in(gokd_session_t s);

/*
 * Request cancellation of a pending WaitForEvent. Safe from ANY thread.
 * The dispatch thread checks this flag in its WaitForEvent loop and
 * returns E_ABORT when set. Cleared automatically after being read.
 */
void gokd_cancel_wait(gokd_session_t s);

/* ====================================================================== */
/*  Memory                                                                */
/* ====================================================================== */

int32_t gokd_read_virtual(gokd_session_t s, uint64_t addr,
                           void *buf, size_t len, size_t *out_read);
int32_t gokd_write_virtual(gokd_session_t s, uint64_t addr,
                            const void *buf, size_t len);
int32_t gokd_read_physical(gokd_session_t s, uint64_t addr,
                            void *buf, size_t len, size_t *out_read);

/* ====================================================================== */
/*  Memory search / translate / query (t1-6)                              */
/* ====================================================================== */

typedef struct gokd_mem_region_s {
    uint64_t base_address;
    uint64_t allocation_base;
    uint32_t allocation_protect;
    uint32_t _pad0;
    uint64_t region_size;
    uint32_t state;
    uint32_t protect;
    uint32_t type;
    uint32_t _pad1;
} gokd_mem_region_t;

/*
 * SearchVirtual scans [start, start+length) for the byte pattern. The
 * pattern_granularity must be 1, 4, or 8 per DbgEng docs and controls the
 * stride DbgEng uses while scanning. Returns E_NOTFOUND when no match —
 * DbgEng's underlying NT/Win32 "not found" codes (0xD0000225, 0x80070490)
 * are normalised onto 0x80000002 so callers can branch with errors.Is.
 */
int32_t gokd_search_virtual(gokd_session_t s,
                             uint64_t start,
                             uint64_t length,
                             const uint8_t *pattern,
                             uint32_t pattern_size,
                             uint32_t pattern_granularity,
                             uint64_t *out_match);

/*
 * Translate a virtual address to a physical address via
 * IDebugDataSpaces4::VirtualToPhysical. Kernel-mode only — fails with
 * E_NOTIMPL (or similar) in user-mode sessions.
 */
int32_t gokd_virtual_to_physical(gokd_session_t s,
                                  uint64_t va,
                                  uint64_t *out_pa);

/*
 * Query the MEMORY_BASIC_INFORMATION64 record covering va via
 * IDebugDataSpaces4::QueryVirtual.
 */
int32_t gokd_query_virtual(gokd_session_t s,
                            uint64_t va,
                            gokd_mem_region_t *out);

/* ====================================================================== */
/*  Registers                                                             */
/* ====================================================================== */

/*
 * Get all registers. Call with out=NULL to query count first.
 * On entry *count = max slots in out. On exit *count = actual count.
 */
int32_t gokd_get_registers(gokd_session_t s,
                            gokd_register_t *out, uint32_t *count);

int32_t gokd_set_register(gokd_session_t s,
                           const char *name, uint64_t value);

/* ====================================================================== */
/*  Stack                                                                 */
/* ====================================================================== */

/*
 * Get the call stack. On entry *count = max frames. On exit *count =
 * actual frame count.
 */
int32_t gokd_get_stack(gokd_session_t s,
                        gokd_frame_t *out, uint32_t max, uint32_t *count);

/* ====================================================================== */
/*  Symbols                                                               */
/* ====================================================================== */

int32_t gokd_name_to_addr(gokd_session_t s,
                           const char *name, uint64_t *addr);

int32_t gokd_addr_to_name(gokd_session_t s, uint64_t addr,
                           char *name, size_t name_len,
                           uint64_t *displacement);

int32_t gokd_set_symbol_path(gokd_session_t s, const char *path);
int32_t gokd_get_symbol_path(gokd_session_t s, char *out, size_t len);

/*
 * Reload symbols. spec is passed verbatim to ReloadWide — empty string
 * means "reload all that need it", "/f" forces a fresh download, "/f nt"
 * targets a single module. May download from the symbol server and take
 * a long time; route through execWithCancel from Go.
 */
int32_t gokd_reload_symbols(gokd_session_t s, const char *spec);

/* ====================================================================== */
/*  Source lines (t1-3)                                                   */
/* ====================================================================== */

/*
 * Map an address to a source file/line via IDebugSymbols3::GetLineByOffsetWide.
 * Count-then-fetch for the file path: pass file_buf=NULL to get the required
 * UTF-8 byte count (including NUL) in *needed; then call again with a buffer
 * of that size. out_line and out_displacement are always populated on success.
 *
 * Returns E_NOTFOUND when DbgEng reports no line info for the address (E_FAIL
 * or E_NOTIMPL get remapped). Go-side surfaces this as ErrNotFound.
 */
int32_t gokd_addr_to_line(gokd_session_t s,
                           uint64_t address,
                           uint32_t *out_line,
                           uint64_t *out_displacement,
                           char *file_buf,
                           uint32_t cap,
                           uint32_t *needed);

/*
 * Map a (file, line) pair to an address via IDebugSymbols3::GetOffsetByLineWide.
 * The path must match the one DbgEng holds in its PDB (typically a full
 * absolute build-machine path); partial matches fail with E_NOTFOUND.
 */
int32_t gokd_line_to_addr(gokd_session_t s,
                           uint32_t line,
                           const char *file_utf8,
                           uint64_t *out_address);

/* ====================================================================== */
/*  Types (via DbgHelp, resolved locally)                                 */
/* ====================================================================== */

int32_t gokd_get_type_size(gokd_session_t s,
                            const char *module, const char *type_name,
                            uint64_t *size);

int32_t gokd_get_field_offset(gokd_session_t s,
                               const char *module, const char *type_name,
                               const char *field, uint32_t *offset);

/*
 * Get all fields of a type. Call with out=NULL to query count first.
 * On entry *count = max slots in out. On exit *count = actual count.
 */
int32_t gokd_get_type_fields(gokd_session_t s,
                              const char *module, const char *type_name,
                              gokd_field_t *out, uint32_t max,
                              uint32_t *count);

/* ====================================================================== */
/*  Modules                                                               */
/* ====================================================================== */

int32_t gokd_get_modules(gokd_session_t s,
                          gokd_module_t *out, uint32_t max, uint32_t *count);

/* ====================================================================== */
/*  Threads                                                               */
/* ====================================================================== */

int32_t gokd_get_threads(gokd_session_t s,
                          gokd_thread_t *out, uint32_t max, uint32_t *count);

int32_t gokd_set_current_thread(gokd_session_t s, uint32_t sys_tid);

/* ====================================================================== */
/*  Breakpoints                                                           */
/* ====================================================================== */

int32_t gokd_add_breakpoint(gokd_session_t s,
                             uint64_t addr, uint32_t *out_id);

int32_t gokd_add_breakpoint_sym(gokd_session_t s,
                                 const char *symbol, uint32_t *out_id);

int32_t gokd_remove_breakpoint(gokd_session_t s, uint32_t id);

int32_t gokd_enable_breakpoint(gokd_session_t s, uint32_t id, int enable);

int32_t gokd_list_breakpoints(gokd_session_t s,
                               gokd_bp_t *out, uint32_t max,
                               uint32_t *count);

/* ====================================================================== */
/*  Disassembly                                                           */
/* ====================================================================== */

int32_t gokd_disassemble(gokd_session_t s, uint64_t addr,
                          char *out, size_t len, uint64_t *next_addr);

/* ====================================================================== */
/*  Expression evaluation (t1-1)                                          */
/* ====================================================================== */

/*
 * Evaluate a MASM/C++ expression. desired_type may be GOKD_VALUE_INVALID
 * for "natural" type, otherwise one of GOKD_VALUE_*. out_value is required
 * and is fully populated on success. out_remainder, when non-NULL, receives
 * the number of wide characters that remain unconsumed after the parsed
 * expression (0 means the whole expression was consumed).
 */
int32_t gokd_evaluate(gokd_session_t s,
                       const char *expr_utf8,
                       uint32_t desired_type,
                       gokd_value_t *out_value,
                       uint32_t *out_remainder);

int32_t gokd_get_radix(gokd_session_t s, uint32_t *out_radix);
int32_t gokd_set_radix(gokd_session_t s, uint32_t radix);

/* Returns DEBUG_EXPR_* in *out_index. */
int32_t gokd_get_expression_syntax(gokd_session_t s, uint32_t *out_index);

/* name is "MASM" or "C++"; mirrors SetExpressionSyntaxByNameWide. */
int32_t gokd_set_expression_syntax(gokd_session_t s, const char *name_utf8);

/* ====================================================================== */
/*  Callbacks                                                             */
/* ====================================================================== */

void gokd_set_event_callback(gokd_session_t s, gokd_event_fn cb, void *ctx);
void gokd_set_output_callback(gokd_session_t s, gokd_output_fn cb, void *ctx);

/* ====================================================================== */
/*  Escape hatch                                                          */
/* ====================================================================== */

/*
 * Execute a raw DbgEng command (like the WinDbg command window).
 * Captured output is written to out (UTF-8, null-terminated).
 * Use sparingly — prefer the typed APIs above.
 */
int32_t gokd_execute(gokd_session_t s, const char *cmd,
                      char *out, size_t out_len);

#ifdef __cplusplus
}
#endif
