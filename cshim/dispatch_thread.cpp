/*
 * dispatch_thread.cpp — DbgEng dispatch thread and session management.
 *
 * DbgEng has strict thread affinity: all calls (including WaitForEvent)
 * must be made from the thread that called DebugCreate. This file
 * implements the session struct holding all COM interfaces and a
 * helper to initialise COM + DbgEng on the calling thread.
 *
 * In the GoKD architecture the "dispatch thread" is actually a Go
 * goroutine pinned with runtime.LockOSThread(). The goroutine calls
 * gokd_create_session (which does COM + DebugCreate) and then calls
 * individual gokd_* functions sequentially — so all DbgEng calls
 * happen on the same OS thread. No C-side threading is needed.
 */

#include <windows.h>
#include <objbase.h>

/* Suppress macro collisions with names we use. */
#undef CreateProcess

#include <dbgeng.h>
#include <dbghelp.h>

#include <cstdlib>
#include <cstring>
#include <mutex>
#include <atomic>

#include "gokd_shim.h"

/* ====================================================================== */
/*  Session state                                                         */
/* ====================================================================== */

struct gokd_session {
    /* COM interfaces obtained via QueryInterface from the initial client. */
    IDebugClient5      *client;
    IDebugControl4     *control;
    IDebugDataSpaces4  *data_spaces;
    IDebugSymbols3     *symbols;
    IDebugRegisters2   *registers;
    IDebugSystemObjects4 *sys_objects;
    IDebugAdvanced3    *advanced;

    /* Callbacks (implemented in callbacks.cpp). */
    IDebugEventCallbacksWide *event_cbs_impl;  /* our implementation */
    IDebugOutputCallbacksWide *output_cbs_impl;

    /* User-registered Go callbacks. */
    gokd_event_fn  event_fn;
    void          *event_ctx;
    gokd_output_fn output_fn;
    void          *output_ctx;

    /* Last stop event captured by callbacks during WaitForEvent. */
    gokd_stop_event_t last_stop;

    /* Most recent HRESULT from a failed call. */
    int32_t last_error;

    /* Whether COM was initialised by us on this thread. */
    int com_initialised;
};

/* ====================================================================== */
/*  Forward declarations (callbacks.cpp)                                  */
/* ====================================================================== */

extern IDebugEventCallbacksWide *gokd_create_event_callbacks(gokd_session *s);
extern IDebugOutputCallbacksWide *gokd_create_output_callbacks(gokd_session *s);
extern void gokd_destroy_event_callbacks(IDebugEventCallbacksWide *cbs);
extern void gokd_destroy_output_callbacks(IDebugOutputCallbacksWide *cbs);

/* ====================================================================== */
/*  Internal: UTF-8 ↔ UTF-16 conversion                                  */
/* ====================================================================== */

/*
 * Convert a UTF-8 string to a newly allocated wchar_t* (UTF-16).
 * Caller must free() the result.  Returns NULL on failure.
 */
wchar_t *utf8_to_wide(const char *utf8) {
    if (!utf8) return NULL;
    int len = MultiByteToWideChar(CP_UTF8, 0, utf8, -1, NULL, 0);
    if (len <= 0) return NULL;
    wchar_t *buf = (wchar_t *)malloc(len * sizeof(wchar_t));
    if (!buf) return NULL;
    MultiByteToWideChar(CP_UTF8, 0, utf8, -1, buf, len);
    return buf;
}

/*
 * Convert a UTF-16 string to a UTF-8 buffer. Returns the number of
 * bytes written (excluding NUL), or 0 on failure.
 */
int wide_to_utf8(const wchar_t *wide, char *out, size_t out_len) {
    if (!wide || !out || out_len == 0) return 0;
    int len = WideCharToMultiByte(CP_UTF8, 0, wide, -1, out, (int)out_len,
                                   NULL, NULL);
    if (len <= 0) {
        out[0] = '\0';
        return 0;
    }
    return len - 1; /* exclude NUL */
}

/*
 * Copy a wide string into a fixed-size UTF-8 char buffer, truncating
 * safely.
 */
void wide_to_utf8_fixed(const wchar_t *wide, char *out, size_t out_size) {
    if (!out || out_size == 0) return;
    if (!wide) { out[0] = '\0'; return; }
    WideCharToMultiByte(CP_UTF8, 0, wide, -1, out, (int)out_size, NULL, NULL);
    out[out_size - 1] = '\0'; /* ensure NUL-termination */
}

/* ====================================================================== */
/*  Session creation / destruction                                        */
/* ====================================================================== */

extern "C" gokd_session_t gokd_create_session(void) {
    gokd_session *s = (gokd_session *)calloc(1, sizeof(gokd_session));
    if (!s) return 0;

    /* Initialise COM on this thread (MTA for DbgEng). */
    HRESULT hr = CoInitializeEx(NULL, COINIT_MULTITHREADED);
    if (SUCCEEDED(hr) || hr == S_FALSE /* already initialised */) {
        s->com_initialised = 1;
    } else {
        free(s);
        return 0;
    }

    /* Create the IDebugClient via DebugCreate. */
    hr = DebugCreate(__uuidof(IDebugClient5), (void **)&s->client);
    if (FAILED(hr)) {
        if (s->com_initialised) CoUninitialize();
        free(s);
        return 0;
    }

    /* QueryInterface for the other interfaces we need. */
    hr = s->client->QueryInterface(__uuidof(IDebugControl4),
                                    (void **)&s->control);
    if (FAILED(hr)) goto fail;

    hr = s->client->QueryInterface(__uuidof(IDebugDataSpaces4),
                                    (void **)&s->data_spaces);
    if (FAILED(hr)) goto fail;

    hr = s->client->QueryInterface(__uuidof(IDebugSymbols3),
                                    (void **)&s->symbols);
    if (FAILED(hr)) goto fail;

    hr = s->client->QueryInterface(__uuidof(IDebugRegisters2),
                                    (void **)&s->registers);
    if (FAILED(hr)) goto fail;

    hr = s->client->QueryInterface(__uuidof(IDebugSystemObjects4),
                                    (void **)&s->sys_objects);
    if (FAILED(hr)) goto fail;

    hr = s->client->QueryInterface(__uuidof(IDebugAdvanced3),
                                    (void **)&s->advanced);
    if (FAILED(hr)) goto fail;

    /* Install our event and output callbacks. */
    s->event_cbs_impl = gokd_create_event_callbacks(s);
    if (s->event_cbs_impl) {
        s->client->SetEventCallbacksWide(s->event_cbs_impl);
    }

    s->output_cbs_impl = gokd_create_output_callbacks(s);
    if (s->output_cbs_impl) {
        s->client->SetOutputCallbacksWide(s->output_cbs_impl);
    }

    return (gokd_session_t)(uintptr_t)s;

fail:
    /* Release whatever we managed to obtain. */
    if (s->advanced)     s->advanced->Release();
    if (s->sys_objects)  s->sys_objects->Release();
    if (s->registers)    s->registers->Release();
    if (s->symbols)      s->symbols->Release();
    if (s->data_spaces)  s->data_spaces->Release();
    if (s->control)      s->control->Release();
    if (s->client)       s->client->Release();
    if (s->com_initialised) CoUninitialize();
    free(s);
    return 0;
}

extern "C" void gokd_destroy_session(gokd_session_t handle) {
    if (!handle) return;
    gokd_session *s = (gokd_session *)(uintptr_t)handle;

    /* Detach from any active target. */
    if (s->client) {
        s->client->SetEventCallbacksWide(NULL);
        s->client->SetOutputCallbacksWide(NULL);
        s->client->EndSession(DEBUG_END_PASSIVE);
    }

    if (s->event_cbs_impl)  gokd_destroy_event_callbacks(s->event_cbs_impl);
    if (s->output_cbs_impl) gokd_destroy_output_callbacks(s->output_cbs_impl);

    if (s->advanced)     s->advanced->Release();
    if (s->sys_objects)  s->sys_objects->Release();
    if (s->registers)    s->registers->Release();
    if (s->symbols)      s->symbols->Release();
    if (s->data_spaces)  s->data_spaces->Release();
    if (s->control)      s->control->Release();
    if (s->client)       s->client->Release();

    if (s->com_initialised) CoUninitialize();

    memset(s, 0, sizeof(*s));
    free(s);
}

extern "C" int32_t gokd_get_last_error(gokd_session_t handle) {
    if (!handle) return E_INVALIDARG;
    gokd_session *s = (gokd_session *)(uintptr_t)handle;
    return s->last_error;
}

/* ====================================================================== */
/*  Internal: get session pointer (used by gokd_shim.cpp)                 */
/* ====================================================================== */

gokd_session *gokd_get_session(gokd_session_t handle) {
    return (gokd_session *)(uintptr_t)handle;
}
