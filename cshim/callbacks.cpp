/*
 * callbacks.cpp — IDebugEventCallbacksWide and IDebugOutputCallbacksWide
 *                 implementations for GoKD.
 *
 * These classes receive events from DbgEng during WaitForEvent and
 * forward them to the user-registered Go callback functions
 * (gokd_event_fn / gokd_output_fn) stored on the session.
 *
 * The callbacks run synchronously on the dispatch thread inside
 * WaitForEvent. The Go side routes them into channels.
 */

#include <cstring>
#include <cstdio>
#include <new>

#include "gokd_internal.h"

#define SESSION_FROM_THIS \
    gokd_session *sess = (gokd_session *)m_session;

/* ====================================================================== */
/*  GokdEventCallbacks                                                    */
/* ====================================================================== */

class GokdEventCallbacks : public IDebugEventCallbacksWide {
public:
    GokdEventCallbacks(gokd_session *session)
        : m_session(session), m_refcount(1) {}
    virtual ~GokdEventCallbacks() {}

    /* IUnknown */
    STDMETHOD_(ULONG, AddRef)() { return ++m_refcount; }
    STDMETHOD_(ULONG, Release)() {
        ULONG r = --m_refcount;
        if (r == 0) delete this;
        return r;
    }
    STDMETHOD(QueryInterface)(REFIID iid, PVOID *out) {
        if (IsEqualIID(iid, __uuidof(IUnknown)) ||
            IsEqualIID(iid, __uuidof(IDebugEventCallbacksWide))) {
            *out = static_cast<IDebugEventCallbacksWide *>(this);
            AddRef();
            return S_OK;
        }
        *out = NULL;
        return E_NOINTERFACE;
    }

    /* IDebugEventCallbacksWide */

    STDMETHOD(GetInterestMask)(PULONG mask) {
        *mask = DEBUG_EVENT_BREAKPOINT |
                DEBUG_EVENT_EXCEPTION |
                DEBUG_EVENT_CREATE_THREAD |
                DEBUG_EVENT_EXIT_THREAD |
                DEBUG_EVENT_CREATE_PROCESS |
                DEBUG_EVENT_EXIT_PROCESS |
                DEBUG_EVENT_LOAD_MODULE |
                DEBUG_EVENT_UNLOAD_MODULE |
                DEBUG_EVENT_SESSION_STATUS |
                DEBUG_EVENT_CHANGE_DEBUGGEE_STATE |
                DEBUG_EVENT_CHANGE_ENGINE_STATE;
        return S_OK;
    }

    STDMETHOD(Breakpoint)(PDEBUG_BREAKPOINT2 bp) {
        SESSION_FROM_THIS;
        /* Capture stop event for the blocking gokd_go/step call. */
        memset(&sess->last_stop, 0, sizeof(sess->last_stop));
        sess->last_stop.reason = GOKD_STOP_BREAKPOINT;

        ULONG bp_id = 0;
        ULONG64 bp_offset = 0;
        if (bp) {
            bp->GetId(&bp_id);
            bp->GetOffset(&bp_offset);
        }
        sess->last_stop.address = bp_offset;

        /* Get current thread. */
        if (sess->sys_objects) {
            ULONG tid = 0;
            sess->sys_objects->GetCurrentThreadSystemId(&tid);
            sess->last_stop.thread_sys_id = tid;
        }

        /* Fire user callback. */
        if (sess->event_fn) {
            gokd_ev_breakpoint_t ev = {};
            ev.bp_id = bp_id;
            ev.address = bp_offset;
            ev.thread_sys_id = sess->last_stop.thread_sys_id;
            sess->event_fn((gokd_session_t)(uintptr_t)sess,
                           GOKD_EVENT_BREAKPOINT, &ev, sess->event_ctx);
        }

        return DEBUG_STATUS_BREAK;
    }

    STDMETHOD(Exception)(PEXCEPTION_RECORD64 exception, ULONG first_chance) {
        SESSION_FROM_THIS;
        memset(&sess->last_stop, 0, sizeof(sess->last_stop));
        sess->last_stop.reason = GOKD_STOP_EXCEPTION;
        sess->last_stop.exception_code = exception ? exception->ExceptionCode : 0;
        sess->last_stop.exception_first_chance = first_chance;
        sess->last_stop.address = exception ? exception->ExceptionAddress : 0;

        if (sess->sys_objects) {
            ULONG tid = 0;
            sess->sys_objects->GetCurrentThreadSystemId(&tid);
            sess->last_stop.thread_sys_id = tid;
        }

        if (sess->event_fn) {
            gokd_ev_exception_t ev = {};
            ev.code = sess->last_stop.exception_code;
            ev.address = sess->last_stop.address;
            ev.first_chance = first_chance;
            ev.thread_sys_id = sess->last_stop.thread_sys_id;
            sess->event_fn((gokd_session_t)(uintptr_t)sess,
                           GOKD_EVENT_EXCEPTION, &ev, sess->event_ctx);
        }

        return DEBUG_STATUS_BREAK;
    }

    STDMETHOD(CreateThread)(ULONG64 handle, ULONG64 data_offset,
                            ULONG64 start_offset) {
        SESSION_FROM_THIS;
        if (sess->event_fn) {
            gokd_ev_thread_created_t ev = {};
            if (sess->sys_objects) {
                ULONG tid = 0;
                sess->sys_objects->GetCurrentThreadSystemId(&tid);
                ev.sys_id = tid;
            }
            ev.handle = handle;
            ev.data_offset = data_offset;
            ev.start_offset = start_offset;
            sess->event_fn((gokd_session_t)(uintptr_t)sess,
                           GOKD_EVENT_THREAD_CREATED, &ev, sess->event_ctx);
        }
        return DEBUG_STATUS_NO_CHANGE;
    }

    STDMETHOD(ExitThread)(ULONG exit_code) {
        SESSION_FROM_THIS;
        if (sess->event_fn) {
            gokd_ev_thread_exited_t ev = {};
            if (sess->sys_objects) {
                ULONG tid = 0;
                sess->sys_objects->GetCurrentThreadSystemId(&tid);
                ev.sys_id = tid;
            }
            ev.exit_code = exit_code;
            sess->event_fn((gokd_session_t)(uintptr_t)sess,
                           GOKD_EVENT_THREAD_EXITED, &ev, sess->event_ctx);
        }
        return DEBUG_STATUS_NO_CHANGE;
    }

    STDMETHOD(CreateProcess)(ULONG64 image_file_handle, ULONG64 handle,
                             ULONG64 base_offset, ULONG module_size,
                             PCWSTR module_name, PCWSTR image_name,
                             ULONG checksum, ULONG timestamp,
                             ULONG64 initial_thread_handle,
                             ULONG64 thread_data_offset,
                             ULONG64 start_offset) {
        SESSION_FROM_THIS;
        if (sess->event_fn) {
            gokd_ev_proc_created_t ev = {};
            ev.base_offset = base_offset;
            ev.module_size = module_size;
            wide_to_utf8_fixed(module_name, ev.module_name,
                               sizeof(ev.module_name));
            wide_to_utf8_fixed(image_name, ev.image_name,
                               sizeof(ev.image_name));
            sess->event_fn((gokd_session_t)(uintptr_t)sess,
                           GOKD_EVENT_PROC_CREATED, &ev, sess->event_ctx);
        }
        return DEBUG_STATUS_NO_CHANGE;
    }

    STDMETHOD(ExitProcess)(ULONG exit_code) {
        SESSION_FROM_THIS;
        /* Mark the stop reason. */
        memset(&sess->last_stop, 0, sizeof(sess->last_stop));
        sess->last_stop.reason = GOKD_STOP_PROC_EXIT;

        if (sess->event_fn) {
            gokd_ev_proc_exited_t ev = {};
            ev.exit_code = exit_code;
            sess->event_fn((gokd_session_t)(uintptr_t)sess,
                           GOKD_EVENT_PROC_EXITED, &ev, sess->event_ctx);
        }
        return DEBUG_STATUS_BREAK;
    }

    STDMETHOD(LoadModule)(ULONG64 image_file_handle, ULONG64 base_offset,
                          ULONG module_size, PCWSTR module_name,
                          PCWSTR image_name, ULONG checksum,
                          ULONG timestamp) {
        SESSION_FROM_THIS;
        if (sess->event_fn) {
            gokd_ev_mod_loaded_t ev = {};
            ev.base_offset = base_offset;
            ev.module_size = module_size;
            wide_to_utf8_fixed(module_name, ev.module_name,
                               sizeof(ev.module_name));
            wide_to_utf8_fixed(image_name, ev.image_name,
                               sizeof(ev.image_name));
            sess->event_fn((gokd_session_t)(uintptr_t)sess,
                           GOKD_EVENT_MOD_LOADED, &ev, sess->event_ctx);
        }
        return DEBUG_STATUS_NO_CHANGE;
    }

    STDMETHOD(UnloadModule)(PCWSTR image_base_name, ULONG64 base_offset) {
        SESSION_FROM_THIS;
        if (sess->event_fn) {
            gokd_ev_mod_unloaded_t ev = {};
            ev.base_offset = base_offset;
            wide_to_utf8_fixed(image_base_name, ev.image_base_name,
                               sizeof(ev.image_base_name));
            sess->event_fn((gokd_session_t)(uintptr_t)sess,
                           GOKD_EVENT_MOD_UNLOADED, &ev, sess->event_ctx);
        }
        return DEBUG_STATUS_NO_CHANGE;
    }

    STDMETHOD(SystemError)(ULONG error, ULONG level) {
        return DEBUG_STATUS_NO_CHANGE;
    }

    STDMETHOD(SessionStatus)(ULONG status) {
        return S_OK;
    }

    STDMETHOD(ChangeDebuggeeState)(ULONG flags, ULONG64 argument) {
        return S_OK;
    }

    STDMETHOD(ChangeEngineState)(ULONG flags, ULONG64 argument) {
        return S_OK;
    }

    STDMETHOD(ChangeSymbolState)(ULONG flags, ULONG64 argument) {
        return S_OK;
    }

private:
    gokd_session *m_session;
    ULONG         m_refcount;
};

/* ====================================================================== */
/*  GokdOutputCallbacks                                                   */
/* ====================================================================== */

class GokdOutputCallbacks : public IDebugOutputCallbacksWide {
public:
    GokdOutputCallbacks(gokd_session *session)
        : m_session(session), m_refcount(1) {}
    virtual ~GokdOutputCallbacks() {}

    /* IUnknown */
    STDMETHOD_(ULONG, AddRef)() { return ++m_refcount; }
    STDMETHOD_(ULONG, Release)() {
        ULONG r = --m_refcount;
        if (r == 0) delete this;
        return r;
    }
    STDMETHOD(QueryInterface)(REFIID iid, PVOID *out) {
        if (IsEqualIID(iid, __uuidof(IUnknown)) ||
            IsEqualIID(iid, __uuidof(IDebugOutputCallbacksWide))) {
            *out = static_cast<IDebugOutputCallbacksWide *>(this);
            AddRef();
            return S_OK;
        }
        *out = NULL;
        return E_NOINTERFACE;
    }

    /* IDebugOutputCallbacksWide */
    STDMETHOD(Output)(ULONG mask, PCWSTR text) {
        SESSION_FROM_THIS;
        if (sess->output_fn && text) {
            /* Convert to UTF-8 for the Go callback. */
            int len = WideCharToMultiByte(CP_UTF8, 0, text, -1,
                                           NULL, 0, NULL, NULL);
            if (len > 0) {
                char *buf = (char *)malloc(len);
                if (buf) {
                    WideCharToMultiByte(CP_UTF8, 0, text, -1,
                                        buf, len, NULL, NULL);
                    sess->output_fn((gokd_session_t)(uintptr_t)sess,
                                    mask, buf, sess->output_ctx);
                    free(buf);
                }
            }
        }
        return S_OK;
    }

private:
    gokd_session *m_session;
    ULONG         m_refcount;
};

/* ====================================================================== */
/*  Factory functions (called from dispatch_thread.cpp)                   */
/* ====================================================================== */

IDebugEventCallbacksWide *gokd_create_event_callbacks(gokd_session *s) {
    return new (std::nothrow) GokdEventCallbacks(s);
}

IDebugOutputCallbacksWide *gokd_create_output_callbacks(gokd_session *s) {
    return new (std::nothrow) GokdOutputCallbacks(s);
}

void gokd_destroy_event_callbacks(IDebugEventCallbacksWide *cbs) {
    if (cbs) cbs->Release();
}

void gokd_destroy_output_callbacks(IDebugOutputCallbacksWide *cbs) {
    if (cbs) cbs->Release();
}
