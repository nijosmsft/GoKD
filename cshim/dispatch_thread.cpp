/*
 * dispatch_thread.cpp — DbgEng session management with dynamic DLL loading.
 *
 * DbgEng has strict thread affinity: all calls (including WaitForEvent)
 * must be made from the thread that called DebugCreate. In the GoKD
 * architecture, the "dispatch thread" is a Go goroutine pinned with
 * runtime.LockOSThread(). It calls gokd_create_session() and then
 * individual gokd_* functions sequentially.
 *
 * CRITICAL: dbgeng.dll is loaded dynamically via LoadLibraryA with
 * an explicit full path to the Windows SDK Debugging Tools version.
 * We do NOT link against dbgeng.lib statically. This ensures the SDK
 * version (which includes KDNET transport for kernel debugging) is
 * loaded instead of the system version (which lacks transport DLLs).
 *
 * The only symbol we need from dbgeng.dll is DebugCreate. All other
 * interactions go through COM vtables, which automatically route to
 * whichever DLL created the COM objects.
 */

#include <cstdlib>
#include <cstring>
#include <cstdio>

#include "gokd_internal.h"

/* ====================================================================== */
/*  Internal: UTF-8 <-> UTF-16 conversion                                */
/* ====================================================================== */

wchar_t *utf8_to_wide(const char *utf8) {
    if (!utf8) return NULL;
    int len = MultiByteToWideChar(CP_UTF8, 0, utf8, -1, NULL, 0);
    if (len <= 0) return NULL;
    wchar_t *buf = (wchar_t *)malloc(len * sizeof(wchar_t));
    if (!buf) return NULL;
    MultiByteToWideChar(CP_UTF8, 0, utf8, -1, buf, len);
    return buf;
}

int wide_to_utf8(const wchar_t *wide, char *out, size_t out_len) {
    if (!wide || !out || out_len == 0) return 0;
    int len = WideCharToMultiByte(CP_UTF8, 0, wide, -1, out, (int)out_len,
                                   NULL, NULL);
    if (len <= 0) { out[0] = '\0'; return 0; }
    return len - 1;
}

void wide_to_utf8_fixed(const wchar_t *wide, char *out, size_t out_size) {
    if (!out || out_size == 0) return;
    if (!wide) { out[0] = '\0'; return; }
    WideCharToMultiByte(CP_UTF8, 0, wide, -1, out, (int)out_size, NULL, NULL);
    out[out_size - 1] = '\0';
}

/* ====================================================================== */
/*  Dynamic DebugCreate loading                                           */
/* ====================================================================== */

typedef HRESULT (STDAPICALLTYPE *PFN_DebugCreate)(REFIID, PVOID*);

static HMODULE      g_hDbgEng = NULL;
static PFN_DebugCreate g_pfnDebugCreate = NULL;

/*
 * Load the SDK's dbgeng.dll dynamically. Search order:
 *   1. GOKD_DBGENG_PATH env var (explicit full path to dbgeng.dll)
 *   2. Common SDK Debugging Tools for Windows locations
 *   3. The directory containing the current executable
 *   4. Standard DLL search (last resort — gets the system version)
 *
 * Returns true on success.
 */
static bool load_dbgeng(void) {
    if (g_hDbgEng) return true;

    /* 1. Explicit override. */
    char env_path[512] = {};
    if (GetEnvironmentVariableA("GOKD_DBGENG_PATH", env_path,
                                 sizeof(env_path)) > 0) {
        g_hDbgEng = LoadLibraryA(env_path);
    }

    /* 2. SDK Debugging Tools for Windows (common paths). */
    if (!g_hDbgEng) {
        static const char *sdk_paths[] = {
            "C:\\Program Files (x86)\\Windows Kits\\10\\Debuggers\\x64\\dbgeng.dll",
            "C:\\Program Files\\Windows Kits\\10\\Debuggers\\x64\\dbgeng.dll",
            "C:\\Debuggers\\dbgeng.dll",
            NULL
        };
        for (int i = 0; sdk_paths[i]; i++) {
            g_hDbgEng = LoadLibraryA(sdk_paths[i]);
            if (g_hDbgEng) break;
        }
    }

    /* 3. Same directory as the running executable. */
    if (!g_hDbgEng) {
        char exe_dir[512] = {};
        DWORD len = GetModuleFileNameA(NULL, exe_dir, sizeof(exe_dir));
        if (len > 0) {
            /* Strip filename to get directory. */
            char *last_sep = strrchr(exe_dir, '\\');
            if (last_sep) {
                *(last_sep + 1) = '\0';
                strncat(exe_dir, "dbgeng.dll", sizeof(exe_dir) - strlen(exe_dir) - 1);
                g_hDbgEng = LoadLibraryA(exe_dir);
            }
        }
    }

    /* 4. Standard search (system32 fallback). */
    if (!g_hDbgEng) {
        g_hDbgEng = LoadLibraryA("dbgeng.dll");
    }

    if (!g_hDbgEng) {
        fprintf(stderr, "[gokd] ERROR: failed to load dbgeng.dll\n");
        return false;
    }

    /* Resolve DebugCreate. */
    g_pfnDebugCreate = (PFN_DebugCreate)(void*)GetProcAddress(
        g_hDbgEng, "DebugCreate");
    if (!g_pfnDebugCreate) {
        fprintf(stderr, "[gokd] ERROR: DebugCreate not found in dbgeng.dll\n");
        FreeLibrary(g_hDbgEng);
        g_hDbgEng = NULL;
        return false;
    }

    /* Log which DLL was loaded. */
    char dll_path[512] = {};
    GetModuleFileNameA(g_hDbgEng, dll_path, sizeof(dll_path));
    fprintf(stderr, "[gokd] loaded dbgeng.dll from: %s\n", dll_path);
    return true;
}

/* ====================================================================== */
/*  Session creation / destruction                                        */
/* ====================================================================== */

extern "C" gokd_session_t gokd_create_session(void) {
    gokd_session *s = (gokd_session *)calloc(1, sizeof(gokd_session));
    if (!s) return 0;

    /* DbgEng requires COM. The header has always documented that we
     * call CoInitializeEx(MTA); keep the implementation in sync. */
    HRESULT cohr = CoInitializeEx(NULL, COINIT_MULTITHREADED);
    if (cohr == RPC_E_CHANGED_MODE) {
        free(s);
        return 0;
    }
    s->com_initialised = SUCCEEDED(cohr);

    /* Load dbgeng.dll dynamically (once per process). */
    if (!load_dbgeng()) {
        if (s->com_initialised) CoUninitialize();
        free(s);
        return 0;
    }

    /* Call DebugCreate through the dynamically resolved function pointer.
     * Request IDebugClient5 directly for the full vtable. */
    HRESULT hr = g_pfnDebugCreate(__uuidof(IDebugClient5),
                                    (void **)&s->client);
    if (FAILED(hr)) {
        fprintf(stderr, "[gokd] DebugCreate failed: 0x%08x\n", (unsigned)hr);
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
    if (s->event_cbs_impl)
        s->client->SetEventCallbacksWide(s->event_cbs_impl);

    s->output_cbs_impl = gokd_create_output_callbacks(s);
    if (s->output_cbs_impl)
        s->client->SetOutputCallbacksWide(s->output_cbs_impl);

    return (gokd_session_t)(uintptr_t)s;

fail:
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
/*  Internal: get session pointer                                         */
/* ====================================================================== */

gokd_session *gokd_get_session(gokd_session_t handle) {
    return (gokd_session *)(uintptr_t)handle;
}
