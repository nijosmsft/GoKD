/*
 * gokd_shim.cpp — Implementation of every gokd_* C API function.
 *
 * Each function retrieves the session pointer, calls the corresponding
 * DbgEng COM method, and translates results into the flat C structs
 * defined in gokd_shim.h.
 *
 * All functions must be called from the dispatch thread (the thread
 * that called gokd_create_session), except gokd_break_in which is
 * explicitly thread-safe.
 */

#include <cstdlib>
#include <cstring>
#include <cstdio>
#include <string>

#include "gokd_internal.h"

/* Access the session's COM interfaces. */
#define S gokd_session *s = gokd_get_session(handle); \
         if (!s) return E_INVALIDARG;

#define SET_LAST_ERROR(hr) do { if (FAILED(hr)) s->last_error = hr; } while(0)

/*
 * Note: SEH (__try/__except) is MSVC-specific and not available in
 * MinGW g++. DbgEng internally handles most exceptions via its own
 * SEH handlers in WaitForEvent. If MSVC builds are needed later,
 * SEH wrappers can be re-added.
 */

/* ====================================================================== */
/*  Internal: cancellable WaitForEvent                                    */
/* ====================================================================== */

/*
 * Wait for a debug event with cancellation support.
 * Uses a loop of short WaitForEvent calls, checking the cancel flag
 * between iterations. For truly indefinite waits (kernel debugging),
 * this allows the Go side to cancel via gokd_cancel_wait().
 *
 * timeout_ms: total timeout in milliseconds, or 0 for infinite.
 * Returns S_OK on event, E_ABORT if cancelled, or the last HRESULT.
 */
static HRESULT wait_for_event_cancellable(gokd_session *s, ULONG timeout_ms) {
    const ULONG POLL_INTERVAL = 500; /* ms between cancel checks */
    ULONG elapsed = 0;

    for (;;) {
        /* Check cancel flag. */
        if (s->cancel_requested) {
            s->cancel_requested = 0;
            /* Force a break-in so the engine settles. */
            s->control->SetInterrupt(DEBUG_INTERRUPT_ACTIVE);
            return E_ABORT;
        }

        /* Compute this iteration's timeout. */
        ULONG this_timeout;
        if (timeout_ms == 0) {
            /* Infinite: poll forever with short intervals. */
            this_timeout = POLL_INTERVAL;
        } else {
            ULONG remaining = timeout_ms - elapsed;
            this_timeout = (remaining < POLL_INTERVAL) ? remaining : POLL_INTERVAL;
        }

        HRESULT hr = s->control->WaitForEvent(DEBUG_WAIT_DEFAULT, this_timeout);
        if (hr == S_OK) return S_OK;   /* Event received. */
        /* S_FALSE = timeout on this iteration. Any failure is terminal. */
        if (FAILED(hr) && hr != (HRESULT)S_FALSE)
            return hr;

        elapsed += this_timeout;
        if (timeout_ms != 0 && elapsed >= timeout_ms)
            return S_FALSE; /* Overall timeout. */
    }
}

/* ====================================================================== */
/*  Attach modes                                                          */
/* ====================================================================== */

extern "C" int32_t gokd_attach_process(gokd_session_t handle,
                                        uint32_t pid, uint32_t flags) {
    S;
    HRESULT hr;

    s->control->AddEngineOptions(DEBUG_ENGOPT_INITIAL_BREAK);

    hr = s->client->AttachProcess(s->remote_server, pid, flags);
    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    hr = wait_for_event_cancellable(s, 30000);
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_create_process(gokd_session_t handle,
                                        const char *cmd, uint32_t flags,
                                        int initial_break) {
    S;
    HRESULT hr;

    /* DbgEng's create-process flow always stops on the initial break;
     * the engine option DEBUG_ENGOPT_INITIAL_BREAK controls whether
     * subsequent breakpoint mechanisms also stop. Toggle it based on
     * the caller's request. */
    if (initial_break) {
        s->control->AddEngineOptions(DEBUG_ENGOPT_INITIAL_BREAK);
    } else {
        s->control->RemoveEngineOptions(DEBUG_ENGOPT_INITIAL_BREAK);
    }

    char cmd_copy[4096];
    strncpy(cmd_copy, cmd, sizeof(cmd_copy) - 1);
    cmd_copy[sizeof(cmd_copy) - 1] = '\0';

    hr = s->client->CreateProcessAndAttach(s->remote_server, cmd_copy,
        flags ? flags : DEBUG_ONLY_THIS_PROCESS,
        0, DEBUG_ATTACH_DEFAULT);
    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    hr = wait_for_event_cancellable(s, 30000);
    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    /* If the caller does not want to halt at the initial break, the
     * wait above still returns once the loader stops. Resume the
     * target so they see a running process. */
    if (!initial_break) {
        s->control->SetExecutionStatus(DEBUG_STATUS_GO);
    }

    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_attach_kernel(gokd_session_t handle,
                                       const char *options,
                                       uint32_t flags) {
    S;

    /* Clear any stale cancellation request from a previous operation
     * so it doesn't abort this legitimate wait. */
    s->cancel_requested = 0;

    s->control->AddEngineOptions(DEBUG_ENGOPT_INITIAL_BREAK);

    HRESULT hr = s->client->AttachKernel(DEBUG_ATTACH_KERNEL_CONNECTION, options);
    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    /* Actively request a break-in so the wait below has something to
     * resolve. Without this, kd.exe-style passive waits sit forever on
     * KDNET targets that have nothing to report. SetInterrupt is safe
     * to call here even though the transport may not yet have
     * handshaked — dbgeng queues the interrupt and dispatches it as
     * soon as the link is live. Ignore the return: any failure just
     * means we fall back to the original passive wait behaviour. */
    if (flags & GOKD_KERNEL_INITIAL_BREAK_IN) {
        s->control->SetInterrupt(DEBUG_INTERRUPT_ACTIVE);
    }

    /* Kernel attach can take a long time. We can't use a single
     * INFINITE WaitForEvent because the Go side has no way to cancel
     * it — gokd_cancel_wait sets a flag we poll. But finite waits
     * during the KDNET handshake legitimately return E_NOTIMPL until
     * the transport is live, so we treat that as "keep polling" rather
     * than terminal failure. */
    const ULONG POLL_INTERVAL = 500; /* ms */
    for (;;) {
        if (s->cancel_requested) {
            s->cancel_requested = 0;
            s->control->SetInterrupt(DEBUG_INTERRUPT_ACTIVE);
            hr = HRESULT_FROM_WIN32(ERROR_OPERATION_ABORTED);
            SET_LAST_ERROR(hr);
            return hr;
        }
        hr = s->control->WaitForEvent(DEBUG_WAIT_DEFAULT, POLL_INTERVAL);
        if (hr == S_OK) break;                       /* handshake complete */
        if (hr == (HRESULT)S_FALSE) continue;        /* poll timeout */
        if (hr == E_NOTIMPL) continue;               /* KDNET still handshaking */
        if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }
    }

    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_open_dump(gokd_session_t handle, const char *path) {
    S;
    wchar_t *wpath = utf8_to_wide(path);
    if (!wpath) return E_OUTOFMEMORY;

    s->control->AddEngineOptions(DEBUG_ENGOPT_INITIAL_BREAK);

    HRESULT hr;
    hr = s->client->OpenDumpFileWide(wpath, 0);
    free(wpath);
    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    /* Dump loading can be slow for large files; use cancellable wait. */
    hr = wait_for_event_cancellable(s, 0 /* infinite */);
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_write_dump(gokd_session_t handle,
                                    const char *path_utf8,
                                    uint32_t qualifier,
                                    uint32_t format_flags,
                                    const char *comment_utf8) {
    S;
    if (!path_utf8) return E_INVALIDARG;
    wchar_t *wpath = utf8_to_wide(path_utf8);
    if (!wpath) return E_OUTOFMEMORY;
    wchar_t *wcomment = NULL;
    if (comment_utf8 && *comment_utf8) {
        wcomment = utf8_to_wide(comment_utf8);
        if (!wcomment) { free(wpath); return E_OUTOFMEMORY; }
    }
    /* NOTE: blocks the dispatch thread; cannot be interrupted by
     * gokd_cancel_wait, which only polls the cancellable wait loop. */
    HRESULT hr = s->client->WriteDumpFileWide(wpath, 0, (ULONG)qualifier,
                                               (ULONG)format_flags, wcomment);
    free(wpath);
    if (wcomment) free(wcomment);
    SET_LAST_ERROR(hr);
    return hr;
}

/* Returns true if the engine is currently attached to a kernel target. */
bool gokd_is_kernel_session(gokd_session *s) {
    if (!s || !s->control) return false;
    ULONG cls = 0, qual = 0;
    if (FAILED(s->control->GetDebuggeeType(&cls, &qual))) return false;
    return cls == DEBUG_CLASS_KERNEL;
}

/* For a halted kernel target, request resume and drain the resulting
 * event so the KDNET stub continues execution before we close the
 * session. Without this, EndSession leaves the target frozen in the
 * debug stub and the VM appears offline until something reattaches
 * and issues Go. Safe to call on any session: no-ops on non-kernel
 * and silently absorbs errors from already-running targets. */
void gokd_resume_kernel_target(gokd_session *s) {
    if (!gokd_is_kernel_session(s)) return;
    ULONG status = 0;
    if (FAILED(s->control->GetExecutionStatus(&status))) return;
    if (status == DEBUG_STATUS_BREAK) {
        s->control->SetExecutionStatus(DEBUG_STATUS_GO);
        /* Short wait to flush the Go to the target. We don't actually
         * want to block waiting for the next break. */
        s->control->WaitForEvent(DEBUG_WAIT_DEFAULT, 2000);
    }
}

extern "C" int32_t gokd_detach(gokd_session_t handle) {
    S;
    HRESULT hr;
    if (gokd_is_kernel_session(s)) {
        /* Kernel targets: resume then active-detach so the target
         * continues execution. DetachProcesses() is a no-op for
         * kernel sessions. */
        gokd_resume_kernel_target(s);
        hr = s->client->EndSession(DEBUG_END_ACTIVE_DETACH);
    } else {
        hr = s->client->DetachProcesses();
    }
    SET_LAST_ERROR(hr);
    return hr;
}

/* ====================================================================== */
/*  Remote debugging                                                      */
/* ====================================================================== */

extern "C" int32_t gokd_connect_remote(gokd_session_t handle,
                                        const char *connection) {
    S;
    wchar_t *wconn = utf8_to_wide(connection);
    if (!wconn) return E_OUTOFMEMORY;

    ULONG64 server = 0;
    HRESULT hr = s->client->ConnectProcessServerWide(wconn, &server);
    free(wconn);
    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    s->remote_server = server;
    return S_OK;
}

extern "C" int32_t gokd_disconnect_remote(gokd_session_t handle) {
    S;
    if (s->remote_server == 0) return S_OK;

    HRESULT hr = s->client->DisconnectProcessServer(s->remote_server);
    s->remote_server = 0;
    SET_LAST_ERROR(hr);
    return hr;
}

/* ====================================================================== */
/*  Execution control                                                     */
/* ====================================================================== */

static int32_t execute_and_wait(gokd_session *s, ULONG status,
                                gokd_stop_event_t *out) {
    memset(&s->last_stop, 0, sizeof(s->last_stop));
    /* Clear stale cancel-request from a previous op. */
    s->cancel_requested = 0;

    HRESULT hr = s->control->SetExecutionStatus(status);
    if (FAILED(hr)) { s->last_error = hr; return hr; }

    /* Cancellable wait — Go side can call gokd_cancel_wait() or
     * gokd_break_in() to interrupt. */
    hr = wait_for_event_cancellable(s, 0 /* infinite */);
    if (FAILED(hr)) { s->last_error = hr; return hr; }

    /*
     * If the callbacks didn't set a specific reason (e.g. step completed
     * without hitting a breakpoint or exception), infer it from the
     * requested execution status.
     */
    if (s->last_stop.reason == 0) {
        if (status == DEBUG_STATUS_STEP_INTO ||
            status == DEBUG_STATUS_STEP_OVER ||
            status == DEBUG_STATUS_STEP_BRANCH) {
            s->last_stop.reason = GOKD_STOP_STEP;
        }
        /* Retrieve the current instruction pointer. */
        if (s->registers) {
            s->registers->GetInstructionOffset(&s->last_stop.address);
        }
        if (s->sys_objects) {
            ULONG tid = 0;
            s->sys_objects->GetCurrentThreadSystemId(&tid);
            s->last_stop.thread_sys_id = tid;
        }
    }

    if (out) *out = s->last_stop;
    return S_OK;
}

extern "C" int32_t gokd_go(gokd_session_t handle, gokd_stop_event_t *out) {
    S;
    return execute_and_wait(s, DEBUG_STATUS_GO, out);
}

extern "C" int32_t gokd_step_in(gokd_session_t handle,
                                 gokd_stop_event_t *out) {
    S;
    return execute_and_wait(s, DEBUG_STATUS_STEP_INTO, out);
}

extern "C" int32_t gokd_step_over(gokd_session_t handle,
                                   gokd_stop_event_t *out) {
    S;
    return execute_and_wait(s, DEBUG_STATUS_STEP_OVER, out);
}

extern "C" int32_t gokd_step_out(gokd_session_t handle,
                                  gokd_stop_event_t *out) {
    S;
    /* DbgEng doesn't have a direct "step out" status; use Execute. */
    HRESULT hr;
    memset(&s->last_stop, 0, sizeof(s->last_stop));
    s->cancel_requested = 0;

    hr = s->control->Execute(DEBUG_OUTCTL_ALL_CLIENTS, "gu",
                              DEBUG_EXECUTE_NOT_LOGGED);

    if (FAILED(hr)) { s->last_error = hr; return hr; }

    /* Use the cancellable poll wait so a Go-side cancel can interrupt
     * a runaway step-out the same way it can interrupt Go/StepIn/etc. */
    hr = wait_for_event_cancellable(s, 0 /* infinite */);

    if (FAILED(hr)) { s->last_error = hr; return hr; }

    if (s->last_stop.reason == 0) {
        s->last_stop.reason = GOKD_STOP_STEP;
        if (s->sys_objects) {
            ULONG tid = 0;
            s->sys_objects->GetCurrentThreadSystemId(&tid);
            s->last_stop.thread_sys_id = tid;
        }
    }

    if (out) *out = s->last_stop;
    return S_OK;
}

extern "C" int32_t gokd_break_in(gokd_session_t handle) {
    /* Thread-safe: SetInterrupt is documented as callable from any thread. */
    S;
    HRESULT hr = s->control->SetInterrupt(DEBUG_INTERRUPT_ACTIVE);
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" void gokd_cancel_wait(gokd_session_t handle) {
    /* Thread-safe: sets a volatile flag checked by the cancellable
     * polling wait, AND issues SetInterrupt(ACTIVE) directly so a
     * non-cancellable INFINITE WaitForEvent (used by gokd_attach_kernel
     * to drive the initial KDNET handshake) also unblocks. SetInterrupt
     * is documented as callable from any thread. */
    gokd_session *s = gokd_get_session(handle);
    if (!s) return;
    s->cancel_requested = 1;
    if (s->control) s->control->SetInterrupt(DEBUG_INTERRUPT_ACTIVE);
}

/* ====================================================================== */
/*  Memory                                                                */
/* ====================================================================== */

extern "C" int32_t gokd_read_virtual(gokd_session_t handle, uint64_t addr,
                                      void *buf, size_t len,
                                      size_t *out_read) {
    S;
    ULONG bytes_read = 0;
    HRESULT hr;
    
        hr = s->data_spaces->ReadVirtual(addr, buf, (ULONG)len, &bytes_read);

    if (out_read) *out_read = bytes_read;
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_write_virtual(gokd_session_t handle, uint64_t addr,
                                       const void *buf, size_t len) {
    S;
    ULONG bytes_written = 0;
    HRESULT hr;
    
        hr = s->data_spaces->WriteVirtual(addr, (PVOID)buf, (ULONG)len,
                                           &bytes_written);

    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_read_physical(gokd_session_t handle, uint64_t addr,
                                       void *buf, size_t len,
                                       size_t *out_read) {
    S;
    ULONG bytes_read = 0;
    HRESULT hr;
    
        hr = s->data_spaces->ReadPhysical(addr, buf, (ULONG)len, &bytes_read);

    if (out_read) *out_read = bytes_read;
    SET_LAST_ERROR(hr);
    return hr;
}

/* ====================================================================== */
/*  Memory search / translate / query (t1-6)                              */
/* ====================================================================== */

#ifndef GOKD_E_NOTFOUND
#define GOKD_E_NOTFOUND ((int32_t)0x80000002)
#endif

/* DbgEng's SearchVirtual can come back with any of several "not found"
 * HRESULTs depending on the build. Normalise to E_NOTFOUND so the Go
 * layer can map it cleanly to ErrNotFound. */
static int32_t map_search_hr(HRESULT hr) {
    /* HRESULT_FROM_NT(STATUS_NOT_FOUND) */
    if ((uint32_t)hr == 0xD0000225u) return GOKD_E_NOTFOUND;
    /* HRESULT_FROM_WIN32(ERROR_NOT_FOUND) */
    if ((uint32_t)hr == 0x80070490u) return GOKD_E_NOTFOUND;
    /* HRESULT_FROM_NT(STATUS_NO_MORE_ENTRIES) — observed on current SDK builds. */
    if ((uint32_t)hr == 0x9000001Au) return GOKD_E_NOTFOUND;
    return hr;
}

extern "C" int32_t gokd_search_virtual(gokd_session_t handle,
                                        uint64_t start,
                                        uint64_t length,
                                        const uint8_t *pattern,
                                        uint32_t pattern_size,
                                        uint32_t pattern_granularity,
                                        uint64_t *out_match) {
    S;
    if (!pattern || pattern_size == 0 || !out_match) return E_INVALIDARG;
    if (pattern_granularity != 1 && pattern_granularity != 4 &&
        pattern_granularity != 8) {
        return E_INVALIDARG;
    }
    ULONG64 match = 0;
    HRESULT hr = s->data_spaces->SearchVirtual(
        (ULONG64)start, (ULONG64)length,
        (PVOID)pattern, (ULONG)pattern_size,
        (ULONG)pattern_granularity, &match);
    if (SUCCEEDED(hr)) {
        *out_match = (uint64_t)match;
        return S_OK;
    }
    int32_t mapped = map_search_hr(hr);
    SET_LAST_ERROR(mapped);
    return mapped;
}

extern "C" int32_t gokd_virtual_to_physical(gokd_session_t handle,
                                             uint64_t va,
                                             uint64_t *out_pa) {
    S;
    if (!out_pa) return E_INVALIDARG;
    ULONG64 pa = 0;
    HRESULT hr = s->data_spaces->VirtualToPhysical((ULONG64)va, &pa);
    if (SUCCEEDED(hr)) {
        *out_pa = (uint64_t)pa;
        return S_OK;
    }
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_query_virtual(gokd_session_t handle,
                                       uint64_t va,
                                       gokd_mem_region_t *out) {
    S;
    if (!out) return E_INVALIDARG;
    MEMORY_BASIC_INFORMATION64 mbi = {};
    HRESULT hr = s->data_spaces->QueryVirtual((ULONG64)va, &mbi);
    if (SUCCEEDED(hr)) {
        memset(out, 0, sizeof(*out));
        out->base_address = (uint64_t)mbi.BaseAddress;
        out->allocation_base = (uint64_t)mbi.AllocationBase;
        out->allocation_protect = (uint32_t)mbi.AllocationProtect;
        out->region_size = (uint64_t)mbi.RegionSize;
        out->state = (uint32_t)mbi.State;
        out->protect = (uint32_t)mbi.Protect;
        out->type = (uint32_t)mbi.Type;
        return S_OK;
    }
    SET_LAST_ERROR(hr);
    return hr;
}

/* ====================================================================== */
/*  Registers                                                             */
/* ====================================================================== */

extern "C" int32_t gokd_get_registers(gokd_session_t handle,
                                       gokd_register_t *out,
                                       uint32_t *count) {
    S;
    ULONG num_regs = 0;
    HRESULT hr;

    
        hr = s->registers->GetNumberRegisters(&num_regs);

    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    /* If out is NULL, just return the count. */
    if (!out) {
        if (count) *count = num_regs;
        return S_OK;
    }

    uint32_t max = count ? *count : 0;
    uint32_t n = (num_regs < max) ? num_regs : max;

    for (uint32_t i = 0; i < n; i++) {
        memset(&out[i], 0, sizeof(gokd_register_t));

        /* Get register name. */
        wchar_t name_buf[64] = {};
        ULONG name_len = 0;
        DEBUG_REGISTER_DESCRIPTION desc = {};
        
            hr = s->registers->GetDescriptionWide(i, name_buf,
                    sizeof(name_buf)/sizeof(name_buf[0]), &name_len, &desc);
    
        if (SUCCEEDED(hr)) {
            wide_to_utf8_fixed(name_buf, out[i].name, sizeof(out[i].name));
        }

        /* Get register value. */
        DEBUG_VALUE val = {};
        
            hr = s->registers->GetValue(i, &val);
    
        if (SUCCEEDED(hr)) {
            out[i].valid = 1;
            /* Map DEBUG_VALUE_* type to our register type. */
            switch (val.Type) {
            case DEBUG_VALUE_INT8:
                out[i].type = GOKD_REG_TYPE_INT8;
                out[i].value = val.I8;
                break;
            case DEBUG_VALUE_INT16:
                out[i].type = GOKD_REG_TYPE_INT16;
                out[i].value = val.I16;
                break;
            case DEBUG_VALUE_INT32:
                out[i].type = GOKD_REG_TYPE_INT32;
                out[i].value = val.I32;
                break;
            case DEBUG_VALUE_INT64:
                out[i].type = GOKD_REG_TYPE_INT64;
                out[i].value = val.I64;
                break;
            case DEBUG_VALUE_FLOAT32:
                out[i].type = GOKD_REG_TYPE_FLOAT32;
                memcpy(&out[i].value, &val.F32, sizeof(float));
                break;
            case DEBUG_VALUE_FLOAT64:
                out[i].type = GOKD_REG_TYPE_FLOAT64;
                memcpy(&out[i].value, &val.F64, sizeof(double));
                break;
            case DEBUG_VALUE_FLOAT80:
                out[i].type = GOKD_REG_TYPE_FLOAT80;
                memcpy(&out[i].value, val.F80Bytes, 8); /* first 8 bytes */
                break;
            case DEBUG_VALUE_FLOAT128:
                out[i].type = GOKD_REG_TYPE_VECTOR128;
                memcpy(&out[i].value, val.F128Bytes, 8);
                break;
            default:
                out[i].type = GOKD_REG_TYPE_INT64;
                out[i].value = val.I64;
                break;
            }
        }
    }

    if (count) *count = n;
    return S_OK;
}

extern "C" int32_t gokd_set_register(gokd_session_t handle,
                                      const char *name, uint64_t value) {
    S;
    wchar_t *wname = utf8_to_wide(name);
    if (!wname) return E_OUTOFMEMORY;

    /* Find the register index by name. */
    ULONG num_regs = 0;
    HRESULT hr;
    
        hr = s->registers->GetNumberRegisters(&num_regs);

    if (FAILED(hr)) { free(wname); SET_LAST_ERROR(hr); return hr; }

    ULONG target_idx = (ULONG)-1;
    for (ULONG i = 0; i < num_regs; i++) {
        wchar_t buf[64] = {};
        ULONG len = 0;
        DEBUG_REGISTER_DESCRIPTION desc = {};
        s->registers->GetDescriptionWide(i, buf,
                sizeof(buf)/sizeof(buf[0]), &len, &desc);
        if (_wcsicmp(buf, wname) == 0) {
            target_idx = i;
            break;
        }
    }
    free(wname);
    if (target_idx == (ULONG)-1) return E_INVALIDARG;

    DEBUG_VALUE val = {};
    val.Type = DEBUG_VALUE_INT64;
    val.I64 = value;
    
        hr = s->registers->SetValue(target_idx, &val);

    SET_LAST_ERROR(hr);
    return hr;
}

/* ====================================================================== */
/*  Stack                                                                 */
/* ====================================================================== */

extern "C" int32_t gokd_get_stack(gokd_session_t handle,
                                   gokd_frame_t *out, uint32_t max,
                                   uint32_t *count) {
    S;
    if (!out || max == 0) return E_INVALIDARG;

    /* Allocate DbgEng stack frames. */
    DEBUG_STACK_FRAME *frames = (DEBUG_STACK_FRAME *)calloc(
        max, sizeof(DEBUG_STACK_FRAME));
    if (!frames) return E_OUTOFMEMORY;

    ULONG filled = 0;
    HRESULT hr;
    
        hr = s->control->GetStackTrace(0, 0, 0, frames, max, &filled);

    if (FAILED(hr)) {
        free(frames);
        SET_LAST_ERROR(hr);
        return hr;
    }

    for (ULONG i = 0; i < filled; i++) {
        memset(&out[i], 0, sizeof(gokd_frame_t));
        out[i].instruction_offset = frames[i].InstructionOffset;
        out[i].return_offset      = frames[i].ReturnOffset;
        out[i].frame_offset       = frames[i].FrameOffset;
        out[i].stack_offset       = frames[i].StackOffset;

        /* Resolve the symbol for this instruction address. */
        wchar_t sym_buf[512] = {};
        ULONG64 displacement = 0;
        HRESULT shr;
        
            shr = s->symbols->GetNameByOffsetWide(
                frames[i].InstructionOffset, sym_buf,
                sizeof(sym_buf)/sizeof(sym_buf[0]), NULL, &displacement);
        if (SUCCEEDED(shr)) {
            /* Split "module!function" into separate fields. */
            wchar_t *bang = wcschr(sym_buf, L'!');
            if (bang) {
                *bang = L'\0';
                wide_to_utf8_fixed(sym_buf, out[i].module,
                                   sizeof(out[i].module));
                wide_to_utf8_fixed(bang + 1, out[i].function,
                                   sizeof(out[i].function));
            } else {
                wide_to_utf8_fixed(sym_buf, out[i].function,
                                   sizeof(out[i].function));
            }
            out[i].displacement = displacement;
        }

        /* Try to get source line info. */
        wchar_t src_buf[512] = {};
        ULONG line = 0;
        
            shr = s->symbols->GetLineByOffsetWide(
                frames[i].InstructionOffset, &line, src_buf,
                sizeof(src_buf)/sizeof(src_buf[0]), NULL, NULL);
        if (SUCCEEDED(shr)) {
            wide_to_utf8_fixed(src_buf, out[i].source_file,
                               sizeof(out[i].source_file));
            out[i].source_line = line;
        }
    }

    free(frames);
    if (count) *count = filled;
    return S_OK;
}

/* ====================================================================== */
/*  Symbols                                                               */
/* ====================================================================== */

extern "C" int32_t gokd_name_to_addr(gokd_session_t handle,
                                      const char *name, uint64_t *addr) {
    S;
    if (!addr) return E_INVALIDARG;

    wchar_t *wname = utf8_to_wide(name);
    if (!wname) return E_OUTOFMEMORY;

    ULONG64 offset = 0;
    HRESULT hr;
    
        hr = s->symbols->GetOffsetByNameWide(wname, &offset);

    free(wname);
    *addr = offset;
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_addr_to_name(gokd_session_t handle, uint64_t addr,
                                      char *name, size_t name_len,
                                      uint64_t *displacement) {
    S;
    wchar_t buf[1024] = {};
    ULONG64 disp = 0;
    HRESULT hr;
    
        hr = s->symbols->GetNameByOffsetWide(addr, buf,
                sizeof(buf)/sizeof(buf[0]), NULL, &disp);

    if (SUCCEEDED(hr)) {
        wide_to_utf8_fixed(buf, name, name_len);
        if (displacement) *displacement = disp;
    }
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_set_symbol_path(gokd_session_t handle,
                                         const char *path) {
    S;
    wchar_t *wpath = utf8_to_wide(path);
    if (!wpath) return E_OUTOFMEMORY;
    HRESULT hr;
    
        hr = s->symbols->SetSymbolPathWide(wpath);

    free(wpath);
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_get_symbol_path(gokd_session_t handle,
                                         char *out, size_t len) {
    S;
    wchar_t buf[2048] = {};
    HRESULT hr;
    
        hr = s->symbols->GetSymbolPathWide(buf,
                sizeof(buf)/sizeof(buf[0]), NULL);

    if (SUCCEEDED(hr)) {
        wide_to_utf8_fixed(buf, out, len);
    }
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_reload_symbols(gokd_session_t handle,
                                        const char *spec) {
    S;
    /* ReloadWide accepts NULL/empty for "reload everything that needs it".
     * Otherwise the spec is forwarded verbatim (e.g. "/f", "/f nt"). */
    const wchar_t *wptr = L"";
    wchar_t *wspec = NULL;
    if (spec && *spec) {
        wspec = utf8_to_wide(spec);
        if (!wspec) return E_OUTOFMEMORY;
        wptr = wspec;
    }
    HRESULT hr = s->symbols->ReloadWide(wptr);
    if (wspec) free(wspec);
    SET_LAST_ERROR(hr);
    return hr;
}

/* ====================================================================== */
/*  Source lines (t1-3)                                                   */
/* ====================================================================== */

#ifndef GOKD_E_NOTFOUND
#define GOKD_E_NOTFOUND ((int32_t)0x80000002)
#endif

/* Map DbgEng "no line info available" failure codes onto a single E_NOTFOUND
 * sentinel that Go can surface as ErrNotFound. */
static int32_t map_srcline_hr(HRESULT hr) {
    if (hr == E_FAIL || hr == E_NOTIMPL) return GOKD_E_NOTFOUND;
    return hr;
}

extern "C" int32_t gokd_addr_to_line(gokd_session_t handle,
                                      uint64_t address,
                                      uint32_t *out_line,
                                      uint64_t *out_displacement,
                                      char *file_buf,
                                      uint32_t cap,
                                      uint32_t *needed) {
    S;
    if (!out_line) return E_INVALIDARG;

    ULONG line = 0;
    ULONG file_size_wide = 0;
    ULONG64 displacement = 0;

    HRESULT hr = s->symbols->GetLineByOffsetWide(
        (ULONG64)address, &line, NULL, 0, &file_size_wide, &displacement);
    /* Some DbgEng builds return S_FALSE when the buffer is too small and
     * still set FileSize; treat that as success for sizing purposes. */
    if (hr != S_OK && hr != S_FALSE) {
        int32_t mapped = map_srcline_hr(hr);
        SET_LAST_ERROR(mapped);
        return mapped;
    }

    *out_line = (uint32_t)line;
    if (out_displacement) *out_displacement = (uint64_t)displacement;

    /* file_size_wide includes the terminating NUL; if zero we have no file
     * info even though DbgEng reports success — treat as not found. */
    if (file_size_wide == 0) {
        if (needed) *needed = 0;
        if (file_buf && cap > 0) file_buf[0] = '\0';
        return S_OK;
    }

    wchar_t *wbuf = (wchar_t *)malloc(file_size_wide * sizeof(wchar_t));
    if (!wbuf) return E_OUTOFMEMORY;
    ULONG actual_wide = 0;
    hr = s->symbols->GetLineByOffsetWide(
        (ULONG64)address, &line, wbuf, file_size_wide, &actual_wide,
        &displacement);
    if (FAILED(hr)) {
        free(wbuf);
        int32_t mapped = map_srcline_hr(hr);
        SET_LAST_ERROR(mapped);
        return mapped;
    }

    /* Compute UTF-8 byte count (including NUL) for sizing. */
    int u8_len = WideCharToMultiByte(CP_UTF8, 0, wbuf, -1, NULL, 0,
                                      NULL, NULL);
    if (u8_len <= 0) u8_len = 1;
    if (needed) *needed = (uint32_t)u8_len;

    if (file_buf && cap > 0) {
        WideCharToMultiByte(CP_UTF8, 0, wbuf, -1, file_buf, (int)cap,
                            NULL, NULL);
        file_buf[cap - 1] = '\0';
    }
    free(wbuf);
    return S_OK;
}

extern "C" int32_t gokd_line_to_addr(gokd_session_t handle,
                                      uint32_t line,
                                      const char *file_utf8,
                                      uint64_t *out_address) {
    S;
    if (!out_address || !file_utf8) return E_INVALIDARG;

    wchar_t *wfile = utf8_to_wide(file_utf8);
    if (!wfile) return E_OUTOFMEMORY;

    ULONG64 offset = 0;
    HRESULT hr = s->symbols->GetOffsetByLineWide((ULONG)line, wfile, &offset);
    free(wfile);

    if (SUCCEEDED(hr)) {
        *out_address = (uint64_t)offset;
        return S_OK;
    }
    int32_t mapped = map_srcline_hr(hr);
    SET_LAST_ERROR(mapped);
    return mapped;
}

/* ====================================================================== */
/*  Types                                                                 */
/* ====================================================================== */

extern "C" int32_t gokd_get_type_size(gokd_session_t handle,
                                       const char *module,
                                       const char *type_name,
                                       uint64_t *size) {
    S;
    if (!size) return E_INVALIDARG;

    wchar_t *wmod = utf8_to_wide(module);
    wchar_t *wtype = utf8_to_wide(type_name);
    if (!wmod || !wtype) { free(wmod); free(wtype); return E_OUTOFMEMORY; }

    /* Find the module base. */
    ULONG64 mod_base = 0;
    ULONG mod_index = 0;
    HRESULT hr;
    
        hr = s->symbols->GetModuleByModuleNameWide(wmod, 0, &mod_index,
                                                    &mod_base);

    if (FAILED(hr)) { free(wmod); free(wtype); SET_LAST_ERROR(hr); return hr; }

    /* Get the type ID. */
    ULONG type_id = 0;
    
        hr = s->symbols->GetTypeIdWide(mod_base, wtype, &type_id);

    if (FAILED(hr)) { free(wmod); free(wtype); SET_LAST_ERROR(hr); return hr; }

    /* Get the type size. */
    ULONG type_size = 0;
    
        hr = s->symbols->GetTypeSize(mod_base, type_id, &type_size);

    *size = type_size;

    free(wmod);
    free(wtype);
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_get_field_offset(gokd_session_t handle,
                                          const char *module,
                                          const char *type_name,
                                          const char *field,
                                          uint32_t *offset) {
    S;
    if (!offset) return E_INVALIDARG;

    wchar_t *wmod = utf8_to_wide(module);
    wchar_t *wtype = utf8_to_wide(type_name);
    wchar_t *wfield = utf8_to_wide(field);
    if (!wmod || !wtype || !wfield) {
        free(wmod); free(wtype); free(wfield);
        return E_OUTOFMEMORY;
    }

    ULONG64 mod_base = 0;
    ULONG mod_index = 0;
    ULONG type_id = 0;
    ULONG field_offset = 0;
    HRESULT hr;

    hr = s->symbols->GetModuleByModuleNameWide(wmod, 0, &mod_index, &mod_base);
    if (FAILED(hr)) goto done;

    hr = s->symbols->GetTypeIdWide(mod_base, wtype, &type_id);
    if (FAILED(hr)) goto done;

    hr = s->symbols->GetFieldOffsetWide(mod_base, type_id, wfield,
                                         &field_offset);
    *offset = field_offset;

done:
    free(wmod); free(wtype); free(wfield);
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_get_type_fields(gokd_session_t handle,
                                         const char *module,
                                         const char *type_name,
                                         gokd_field_t *out,
                                         uint32_t max, uint32_t *count) {
    S;
    /*
     * DbgEng does not provide a direct "enumerate fields" API.
     * We use IDebugSymbols3::GetTypeId + OutputTypedDataVirtual
     * for a basic field list, or fall back to the DbgHelp
     * SymGetTypeInfo approach for richer results.
     *
     * For now, use the IDebugSymbols3 typed output approach with
     * explicit field enumeration via the type model.
     *
     * TODO: Implement full field enumeration via DbgHelp
     *       SymGetTypeInfo(TI_GET_CHILDRENCOUNT / TI_FINDCHILDREN).
     */

    wchar_t *wmod = utf8_to_wide(module);
    wchar_t *wtype = utf8_to_wide(type_name);
    if (!wmod || !wtype) { free(wmod); free(wtype); return E_OUTOFMEMORY; }

    ULONG64 mod_base = 0;
    ULONG mod_index = 0;
    HRESULT hr;
    
        hr = s->symbols->GetModuleByModuleNameWide(wmod, 0, &mod_index,
                                                    &mod_base);

    if (FAILED(hr)) { free(wmod); free(wtype); SET_LAST_ERROR(hr); return hr; }

    ULONG type_id = 0;
    
        hr = s->symbols->GetTypeIdWide(mod_base, wtype, &type_id);

    if (FAILED(hr)) { free(wmod); free(wtype); SET_LAST_ERROR(hr); return hr; }

    /*
     * Use DbgHelp's SymGetTypeInfo to enumerate children.
     * We call through the DbgEng process handle.
     */
    ULONG64 process_handle = 0;
    
        hr = s->sys_objects->GetCurrentProcessHandle(&process_handle);

    if (FAILED(hr)) { free(wmod); free(wtype); SET_LAST_ERROR(hr); return hr; }

    DWORD child_count = 0;
    if (!SymGetTypeInfo((HANDLE)process_handle, mod_base, type_id,
                        TI_GET_CHILDRENCOUNT, &child_count)) {
        free(wmod); free(wtype);
        hr = HRESULT_FROM_WIN32(GetLastError());
        SET_LAST_ERROR(hr);
        return hr;
    }

    if (!out) {
        /* Caller just wants the count. */
        if (count) *count = child_count;
        free(wmod); free(wtype);
        return S_OK;
    }

    /* Allocate TI_FINDCHILDREN_PARAMS. */
    size_t params_size = sizeof(TI_FINDCHILDREN_PARAMS) +
                         child_count * sizeof(ULONG);
    TI_FINDCHILDREN_PARAMS *params =
        (TI_FINDCHILDREN_PARAMS *)calloc(1, params_size);
    if (!params) { free(wmod); free(wtype); return E_OUTOFMEMORY; }
    params->Count = child_count;
    params->Start = 0;

    if (!SymGetTypeInfo((HANDLE)process_handle, mod_base, type_id,
                        TI_FINDCHILDREN, params)) {
        free(params); free(wmod); free(wtype);
        hr = HRESULT_FROM_WIN32(GetLastError());
        SET_LAST_ERROR(hr);
        return hr;
    }

    uint32_t n = (child_count < max) ? child_count : max;
    for (uint32_t i = 0; i < n; i++) {
        memset(&out[i], 0, sizeof(gokd_field_t));
        ULONG child_id = params->ChildId[i];

        /* Get the field name. */
        WCHAR *field_name = NULL;
        if (SymGetTypeInfo((HANDLE)process_handle, mod_base, child_id,
                           TI_GET_SYMNAME, &field_name)) {
            if (field_name) {
                wide_to_utf8_fixed(field_name, out[i].name,
                                   sizeof(out[i].name));
                LocalFree(field_name);
            }
        }

        /* Get the field offset. */
        DWORD field_offset = 0;
        if (SymGetTypeInfo((HANDLE)process_handle, mod_base, child_id,
                           TI_GET_OFFSET, &field_offset)) {
            out[i].offset = field_offset;
        }

        /* Get the type of the field. */
        DWORD field_type_id = 0;
        if (SymGetTypeInfo((HANDLE)process_handle, mod_base, child_id,
                           TI_GET_TYPEID, &field_type_id)) {
            /* Get the size of the field's type. */
            ULONG64 field_size = 0;
            if (SymGetTypeInfo((HANDLE)process_handle, mod_base,
                               field_type_id, TI_GET_LENGTH, &field_size)) {
                out[i].size = field_size;
            }

            /* Get the type name. */
            WCHAR *type_name_w = NULL;
            if (SymGetTypeInfo((HANDLE)process_handle, mod_base,
                               field_type_id, TI_GET_SYMNAME, &type_name_w)) {
                if (type_name_w) {
                    wide_to_utf8_fixed(type_name_w, out[i].type_name,
                                       sizeof(out[i].type_name));
                    LocalFree(type_name_w);
                }
            }
        }
    }

    free(params);
    free(wmod);
    free(wtype);
    if (count) *count = n;
    return S_OK;
}

/* ====================================================================== */
/*  Modules                                                               */
/* ====================================================================== */

extern "C" int32_t gokd_get_modules(gokd_session_t handle,
                                     gokd_module_t *out, uint32_t max,
                                     uint32_t *count) {
    S;
    ULONG loaded = 0, unloaded = 0;
    HRESULT hr;
    
        hr = s->symbols->GetNumberModules(&loaded, &unloaded);

    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    if (!out) {
        if (count) *count = loaded;
        return S_OK;
    }

    uint32_t n = (loaded < max) ? loaded : max;
    for (uint32_t i = 0; i < n; i++) {
        memset(&out[i], 0, sizeof(gokd_module_t));

        ULONG64 base = 0;
        
            hr = s->symbols->GetModuleByIndex(i, &base);
    
        if (FAILED(hr)) continue;
        out[i].base = base;

        /* Get module parameters. */
        DEBUG_MODULE_PARAMETERS params = {};
        
            hr = s->symbols->GetModuleParameters(1, &base, 0, &params);
    
        if (SUCCEEDED(hr)) {
            out[i].size = params.Size;
            out[i].timestamp = params.TimeDateStamp;
            out[i].checksum = params.Checksum;
            out[i].symbol_type = params.SymbolType;
        }

        /* Get module name. */
        wchar_t name_buf[256] = {};
        wchar_t image_buf[512] = {};
        
            s->symbols->GetModuleNameStringWide(
                DEBUG_MODNAME_MODULE, i, base,
                name_buf, sizeof(name_buf)/sizeof(name_buf[0]), NULL);
            s->symbols->GetModuleNameStringWide(
                DEBUG_MODNAME_IMAGE, i, base,
                image_buf, sizeof(image_buf)/sizeof(image_buf[0]), NULL);
    
        wide_to_utf8_fixed(name_buf, out[i].name, sizeof(out[i].name));
        wide_to_utf8_fixed(image_buf, out[i].image_name,
                           sizeof(out[i].image_name));
    }

    if (count) *count = n;
    return S_OK;
}

/* ====================================================================== */
/*  Threads                                                               */
/* ====================================================================== */

extern "C" int32_t gokd_get_threads(gokd_session_t handle,
                                     gokd_thread_t *out, uint32_t max,
                                     uint32_t *count) {
    S;
    ULONG total = 0;
    HRESULT hr;
    
        hr = s->sys_objects->GetNumberThreads(&total);

    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    if (!out) {
        if (count) *count = total;
        return S_OK;
    }

    uint32_t n = (total < max) ? total : max;

    /* Get system thread IDs. */
    ULONG *ids = (ULONG *)calloc(total, sizeof(ULONG));
    ULONG *sys_ids = (ULONG *)calloc(total, sizeof(ULONG));
    if (!ids || !sys_ids) { free(ids); free(sys_ids); return E_OUTOFMEMORY; }

    
        hr = s->sys_objects->GetThreadIdsByIndex(0, total, ids, sys_ids);

    if (FAILED(hr)) {
        free(ids); free(sys_ids);
        SET_LAST_ERROR(hr);
        return hr;
    }

    /* Save current thread so we can restore it. */
    ULONG saved_thread = 0;
    s->sys_objects->GetCurrentThreadId(&saved_thread);

    for (uint32_t i = 0; i < n; i++) {
        memset(&out[i], 0, sizeof(gokd_thread_t));
        out[i].system_id = sys_ids[i];

        /* Switch to this thread to read handle/TEB. */
        if (SUCCEEDED(s->sys_objects->SetCurrentThreadId(ids[i]))) {
            ULONG64 handle_val = 0;
            s->sys_objects->GetCurrentThreadHandle(&handle_val);
            out[i].handle = handle_val;

            ULONG64 teb = 0;
            s->sys_objects->GetCurrentThreadDataOffset(&teb);
            out[i].data_offset = teb;

            ULONG64 start = 0;
            s->sys_objects->GetCurrentThreadTeb(&start);
            out[i].start_offset = start;
        }
    }

    /* Restore original thread. */
    s->sys_objects->SetCurrentThreadId(saved_thread);

    free(ids);
    free(sys_ids);
    if (count) *count = n;
    return S_OK;
}

extern "C" int32_t gokd_set_current_thread(gokd_session_t handle,
                                             uint32_t sys_tid) {
    S;
    /* Find the engine thread ID from the system thread ID. */
    ULONG engine_id = 0;
    HRESULT hr;
    
        hr = s->sys_objects->GetThreadIdBySystemId(sys_tid, &engine_id);

    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    
        hr = s->sys_objects->SetCurrentThreadId(engine_id);

    SET_LAST_ERROR(hr);
    return hr;
}

/* ====================================================================== */
/*  Breakpoints                                                           */
/* ====================================================================== */

extern "C" int32_t gokd_add_breakpoint(gokd_session_t handle,
                                        uint64_t addr, uint32_t *out_id) {
    S;
    IDebugBreakpoint2 *bp = NULL;
    HRESULT hr;
    
        hr = s->control->AddBreakpoint2(DEBUG_BREAKPOINT_CODE, DEBUG_ANY_ID,
                                         &bp);

    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    
        hr = bp->SetOffset(addr);

    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    
        bp->AddFlags(DEBUG_BREAKPOINT_ENABLED);


    ULONG id = 0;
    bp->GetId(&id);
    if (out_id) *out_id = id;
    return S_OK;
}

extern "C" int32_t gokd_add_breakpoint_sym(gokd_session_t handle,
                                            const char *symbol,
                                            uint32_t *out_id) {
    S;
    wchar_t *wsym = utf8_to_wide(symbol);
    if (!wsym) return E_OUTOFMEMORY;

    IDebugBreakpoint2 *bp = NULL;
    HRESULT hr;
    
        hr = s->control->AddBreakpoint2(DEBUG_BREAKPOINT_CODE, DEBUG_ANY_ID,
                                         &bp);

    if (FAILED(hr)) { free(wsym); SET_LAST_ERROR(hr); return hr; }

    
        hr = bp->SetOffsetExpressionWide(wsym);

    free(wsym);
    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    
        bp->AddFlags(DEBUG_BREAKPOINT_ENABLED);


    ULONG id = 0;
    bp->GetId(&id);
    if (out_id) *out_id = id;
    return S_OK;
}

extern "C" int32_t gokd_remove_breakpoint(gokd_session_t handle,
                                           uint32_t id) {
    S;
    IDebugBreakpoint2 *bp = NULL;
    HRESULT hr;
    
        hr = s->control->GetBreakpointById2(id, &bp);

    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    
        hr = s->control->RemoveBreakpoint2(bp);

    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_enable_breakpoint(gokd_session_t handle,
                                           uint32_t id, int enable) {
    S;
    IDebugBreakpoint2 *bp = NULL;
    HRESULT hr;
    
        hr = s->control->GetBreakpointById2(id, &bp);

    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    
        if (enable)
            hr = bp->AddFlags(DEBUG_BREAKPOINT_ENABLED);
        else
            hr = bp->RemoveFlags(DEBUG_BREAKPOINT_ENABLED);

    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_list_breakpoints(gokd_session_t handle,
                                          gokd_bp_t *out, uint32_t max,
                                          uint32_t *count) {
    S;
    ULONG num_bps = 0;
    HRESULT hr;
    
        hr = s->control->GetNumberBreakpoints(&num_bps);

    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    if (!out) {
        if (count) *count = num_bps;
        return S_OK;
    }

    uint32_t n = (num_bps < max) ? num_bps : max;
    for (uint32_t i = 0; i < n; i++) {
        memset(&out[i], 0, sizeof(gokd_bp_t));

        IDebugBreakpoint2 *bp = NULL;
        
            hr = s->control->GetBreakpointByIndex2(i, &bp);
    
        if (FAILED(hr) || !bp) continue;

        ULONG id = 0;
        bp->GetId(&id);
        out[i].id = id;

        ULONG64 offset = 0;
        bp->GetOffset(&offset);
        out[i].offset = offset;

        wchar_t expr[512] = {};
        bp->GetOffsetExpressionWide(expr,
                sizeof(expr)/sizeof(expr[0]), NULL);
        wide_to_utf8_fixed(expr, out[i].expression,
                           sizeof(out[i].expression));

        ULONG flags = 0;
        bp->GetFlags(&flags);
        out[i].flags = flags;
        out[i].enabled = (flags & DEBUG_BREAKPOINT_ENABLED) ? 1 : 0;
    }

    if (count) *count = n;
    return S_OK;
}

/* ====================================================================== */
/*  Disassembly                                                           */
/* ====================================================================== */

extern "C" int32_t gokd_disassemble(gokd_session_t handle, uint64_t addr,
                                     char *out, size_t len,
                                     uint64_t *next_addr) {
    S;
    wchar_t buf[1024] = {};
    ULONG64 end_offset = 0;
    HRESULT hr;
    
        hr = s->control->DisassembleWide(
            addr, 0, buf, sizeof(buf)/sizeof(buf[0]), NULL, &end_offset);

    if (SUCCEEDED(hr)) {
        wide_to_utf8_fixed(buf, out, len);
        if (next_addr) *next_addr = end_offset;
    }
    SET_LAST_ERROR(hr);
    return hr;
}

/* ====================================================================== */
/*  Expression evaluation (t1-1)                                          */
/* ====================================================================== */

extern "C" int32_t gokd_evaluate(gokd_session_t handle,
                                  const char *expr_utf8,
                                  uint32_t desired_type,
                                  gokd_value_t *out_value,
                                  uint32_t *out_remainder) {
    S;
    if (!out_value || !expr_utf8) return E_INVALIDARG;

    wchar_t *wexpr = utf8_to_wide(expr_utf8);
    if (!wexpr) return E_OUTOFMEMORY;
    size_t wexpr_len = wcslen(wexpr);

    DEBUG_VALUE dv = {};
    ULONG rem = 0;
    HRESULT hr = s->control->EvaluateWide(wexpr, desired_type, &dv, &rem);
    free(wexpr);

    if (SUCCEEDED(hr)) {
        memset(out_value, 0, sizeof(*out_value));
        out_value->type = dv.Type;
        out_value->u64 = dv.I64;
        if (dv.Type == DEBUG_VALUE_FLOAT32) {
            out_value->f64 = (double)dv.F32;
        } else if (dv.Type == DEBUG_VALUE_FLOAT64) {
            out_value->f64 = dv.F64;
        }
        /* RawBytes is the full 24-byte payload union — copies float-80/82/128
         * and vector-64/128 cases that don't fit u64/f64. */
        memcpy(out_value->raw, dv.RawBytes, sizeof(out_value->raw));
        if (out_remainder) {
            /* DbgEng returns the wide-char index at which parsing stopped;
             * convert to an unconsumed-character count so 0 == fully parsed. */
            uint32_t unconsumed = (rem >= wexpr_len) ? 0u
                                                     : (uint32_t)(wexpr_len - rem);
            *out_remainder = unconsumed;
        }
    }
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_get_radix(gokd_session_t handle, uint32_t *out_radix) {
    S;
    if (!out_radix) return E_INVALIDARG;
    ULONG r = 0;
    HRESULT hr = s->control->GetRadix(&r);
    if (SUCCEEDED(hr)) *out_radix = r;
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_set_radix(gokd_session_t handle, uint32_t radix) {
    S;
    HRESULT hr = s->control->SetRadix(radix);
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_get_expression_syntax(gokd_session_t handle,
                                               uint32_t *out_index) {
    S;
    if (!out_index) return E_INVALIDARG;
    ULONG idx = 0;
    HRESULT hr = s->control->GetExpressionSyntax(&idx);
    if (SUCCEEDED(hr)) *out_index = idx;
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_set_expression_syntax(gokd_session_t handle,
                                               const char *name_utf8) {
    S;
    if (!name_utf8) return E_INVALIDARG;
    wchar_t *wname = utf8_to_wide(name_utf8);
    if (!wname) return E_OUTOFMEMORY;
    HRESULT hr = s->control->SetExpressionSyntaxByNameWide(wname);
    free(wname);
    SET_LAST_ERROR(hr);
    return hr;
}

/* ====================================================================== */
/*  Callbacks                                                             */
/* ====================================================================== */

extern "C" void gokd_set_event_callback(gokd_session_t handle,
                                         gokd_event_fn cb, void *ctx) {
    gokd_session *s = gokd_get_session(handle);
    if (!s) return;
    s->event_fn = cb;
    s->event_ctx = ctx;
}

extern "C" void gokd_set_output_callback(gokd_session_t handle,
                                          gokd_output_fn cb, void *ctx) {
    gokd_session *s = gokd_get_session(handle);
    if (!s) return;
    s->output_fn = cb;
    s->output_ctx = ctx;
}

/* ====================================================================== */
/*  Escape hatch                                                          */
/* ====================================================================== */

extern "C" int32_t gokd_execute(gokd_session_t handle, const char *cmd,
                                 char *out, size_t out_len) {
    S;

    wchar_t *wcmd = utf8_to_wide(cmd);
    if (!wcmd) return E_OUTOFMEMORY;

    if (out && out_len > 0) out[0] = '\0';

    /* Per-call output capture: install a private IDebugOutputCallbacksWide
     * that accumulates into a std::wstring, run ExecuteWide, then restore
     * the previous callback. ExecuteWide returns its output exclusively
     * through the registered output callback — capturing here means the
     * caller gets it via the return value AND any GokdOutputCallbacks
     * channel feed is suppressed for the duration of this call (which is
     * the right behaviour: the caller is taking ownership of the output). */
    class OutputCapture : public IDebugOutputCallbacksWide {
    public:
        std::wstring buf;
        ULONG refcount = 1;
        STDMETHOD_(ULONG, AddRef)() { return ++refcount; }
        STDMETHOD_(ULONG, Release)() {
            ULONG r = --refcount;
            /* Stack-allocated — never delete. */
            return r;
        }
        STDMETHOD(QueryInterface)(REFIID iid, PVOID *ppv) {
            if (IsEqualIID(iid, __uuidof(IUnknown)) ||
                IsEqualIID(iid, __uuidof(IDebugOutputCallbacksWide))) {
                *ppv = static_cast<IDebugOutputCallbacksWide *>(this);
                AddRef();
                return S_OK;
            }
            *ppv = nullptr;
            return E_NOINTERFACE;
        }
        STDMETHOD(Output)(ULONG /*mask*/, PCWSTR text) {
            if (text) buf.append(text);
            return S_OK;
        }
    };

    IDebugOutputCallbacksWide *prev = nullptr;
    s->client->GetOutputCallbacksWide(&prev);

    OutputCapture cap;
    s->client->SetOutputCallbacksWide(&cap);

    HRESULT hr = s->control->ExecuteWide(
        DEBUG_OUTCTL_THIS_CLIENT | DEBUG_OUTCTL_OVERRIDE_MASK |
            DEBUG_OUTCTL_NOT_LOGGED,
        wcmd, DEBUG_EXECUTE_DEFAULT);

    /* Restore the previous callback unconditionally, even on failure. */
    s->client->SetOutputCallbacksWide(prev);

    free(wcmd);

    if (out && out_len > 0) {
        wide_to_utf8_fixed(cap.buf.c_str(), out, out_len);
    }

    SET_LAST_ERROR(hr);
    return hr;
}
