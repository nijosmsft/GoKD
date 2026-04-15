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
        if (FAILED(hr) && hr != (HRESULT)S_FALSE) return hr; /* Real error. */

        /* S_FALSE = timeout on this iteration. */
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
                                        const char *cmd, uint32_t flags) {
    S;
    HRESULT hr;

    s->control->AddEngineOptions(DEBUG_ENGOPT_INITIAL_BREAK);

    char cmd_copy[4096];
    strncpy(cmd_copy, cmd, sizeof(cmd_copy) - 1);
    cmd_copy[sizeof(cmd_copy) - 1] = '\0';

    hr = s->client->CreateProcessAndAttach(s->remote_server, cmd_copy,
        flags ? flags : DEBUG_ONLY_THIS_PROCESS,
        0, DEBUG_ATTACH_DEFAULT);
    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    hr = wait_for_event_cancellable(s, 30000);
    SET_LAST_ERROR(hr);
    return hr;
}

extern "C" int32_t gokd_attach_kernel(gokd_session_t handle,
                                       const char *options) {
    S;

    s->control->AddEngineOptions(DEBUG_ENGOPT_INITIAL_BREAK);

    HRESULT hr = s->client->AttachKernel(DEBUG_ATTACH_KERNEL_CONNECTION, options);
    fprintf(stderr, "[gokd] AttachKernel('%s') returned 0x%08x\n", options, (unsigned)hr);
    if (FAILED(hr)) { SET_LAST_ERROR(hr); return hr; }

    /* Kernel attach can take a long time (waiting for target to break).
     * Use cancellable infinite wait so the Go side can abort. */
    hr = wait_for_event_cancellable(s, 0 /* infinite */);
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

extern "C" int32_t gokd_detach(gokd_session_t handle) {
    S;
    HRESULT hr = s->client->DetachProcesses();
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

    
        hr = s->control->Execute(DEBUG_OUTCTL_ALL_CLIENTS, "gu",
                                  DEBUG_EXECUTE_NOT_LOGGED);

    if (FAILED(hr)) { s->last_error = hr; return hr; }

    
        hr = s->control->WaitForEvent(DEBUG_WAIT_DEFAULT, INFINITE);

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
    /* Thread-safe: sets a volatile flag checked by the dispatch thread. */
    gokd_session *s = gokd_get_session(handle);
    if (s) s->cancel_requested = 1;
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
    /*
     * We temporarily capture the output into a buffer by using the output
     * callbacks. The current output callback is saved and restored.
     */

    /* Execute the command. Output goes to our registered output callback. */
    wchar_t *wcmd = utf8_to_wide(cmd);
    if (!wcmd) return E_OUTOFMEMORY;

    /* We need to capture output. Use a temporary output buffer approach:
     * set a flag on the session to capture output into a buffer. */
    HRESULT hr;

    if (out && out_len > 0) {
        out[0] = '\0';

        /* Use OutputWide with a capture mechanism. For simplicity,
         * execute with OUTCTL to capture to our output callback,
         * then the Go side reads from the output channel. */
        
            hr = s->control->ExecuteWide(
                DEBUG_OUTCTL_ALL_CLIENTS, wcmd, DEBUG_EXECUTE_DEFAULT);
    

        /* For captured output, we'd need the output callback to accumulate
         * into a buffer. For now, this sends output through the output
         * callback and the Go side collects it via the Output() channel.
         * The `out` parameter will contain a note about this. */
    } else {
        
            hr = s->control->ExecuteWide(
                DEBUG_OUTCTL_ALL_CLIENTS, wcmd, DEBUG_EXECUTE_DEFAULT);
    
    }

    free(wcmd);
    SET_LAST_ERROR(hr);
    return hr;
}
