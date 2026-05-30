/*
 * test_kernel_with_cb.cpp — like test_kernel_minimal.cpp, but registers
 * IDebugEventCallbacksWide and IDebugOutputCallbacksWide before AttachKernel,
 * mirroring gokd's shim setup. If WaitForEvent now returns E_NOTIMPL instead
 * of blocking, callbacks are the culprit.
 *
 * Build:
 *   g++ -std=c++17 -O2 test_kernel_with_cb.cpp -o test_kernel_with_cb.exe -lole32 -luuid
 */
#include <windows.h>
#include <objbase.h>
#include <dbgeng.h>
#include <cstdio>
#include <cstdlib>
#include <cstring>

typedef HRESULT (STDAPICALLTYPE *PFN_DebugCreate)(REFIID, PVOID*);

class StubEventCB : public IDebugEventCallbacksWide {
    ULONG m_ref = 1;
public:
    STDMETHOD_(ULONG, AddRef)() { return ++m_ref; }
    STDMETHOD_(ULONG, Release)() { ULONG r = --m_ref; if (r == 0) delete this; return r; }
    STDMETHOD(QueryInterface)(REFIID iid, PVOID *out) {
        if (IsEqualIID(iid, __uuidof(IUnknown)) ||
            IsEqualIID(iid, __uuidof(IDebugEventCallbacksWide))) {
            *out = static_cast<IDebugEventCallbacksWide *>(this);
            AddRef(); return S_OK;
        }
        *out = NULL; return E_NOINTERFACE;
    }
    STDMETHOD(GetInterestMask)(PULONG mask) {
        *mask = DEBUG_EVENT_BREAKPOINT | DEBUG_EVENT_EXCEPTION |
                DEBUG_EVENT_CREATE_THREAD | DEBUG_EVENT_EXIT_THREAD |
                DEBUG_EVENT_CREATE_PROCESS | DEBUG_EVENT_EXIT_PROCESS |
                DEBUG_EVENT_LOAD_MODULE | DEBUG_EVENT_UNLOAD_MODULE |
                DEBUG_EVENT_SESSION_STATUS | DEBUG_EVENT_CHANGE_DEBUGGEE_STATE |
                DEBUG_EVENT_CHANGE_ENGINE_STATE;
        return S_OK;
    }
    STDMETHOD(Breakpoint)(PDEBUG_BREAKPOINT2) { return DEBUG_STATUS_BREAK; }
    STDMETHOD(Exception)(PEXCEPTION_RECORD64, ULONG) { return DEBUG_STATUS_BREAK; }
    STDMETHOD(CreateThread)(ULONG64, ULONG64, ULONG64) { return DEBUG_STATUS_NO_CHANGE; }
    STDMETHOD(ExitThread)(ULONG) { return DEBUG_STATUS_NO_CHANGE; }
    STDMETHOD(CreateProcess)(ULONG64, ULONG64, ULONG64, ULONG, PCWSTR, PCWSTR,
                             ULONG, ULONG, ULONG64, ULONG64, ULONG64) { return DEBUG_STATUS_NO_CHANGE; }
    STDMETHOD(ExitProcess)(ULONG) { return DEBUG_STATUS_BREAK; }
    STDMETHOD(LoadModule)(ULONG64, ULONG64, ULONG, PCWSTR, PCWSTR, ULONG, ULONG) { return DEBUG_STATUS_NO_CHANGE; }
    STDMETHOD(UnloadModule)(PCWSTR, ULONG64) { return DEBUG_STATUS_NO_CHANGE; }
    STDMETHOD(SystemError)(ULONG, ULONG) { return DEBUG_STATUS_NO_CHANGE; }
    STDMETHOD(SessionStatus)(ULONG) { return S_OK; }
    STDMETHOD(ChangeDebuggeeState)(ULONG, ULONG64) { return S_OK; }
    STDMETHOD(ChangeEngineState)(ULONG, ULONG64) { return S_OK; }
    STDMETHOD(ChangeSymbolState)(ULONG, ULONG64) { return S_OK; }
};

class StubOutputCB : public IDebugOutputCallbacksWide {
    ULONG m_ref = 1;
public:
    STDMETHOD_(ULONG, AddRef)() { return ++m_ref; }
    STDMETHOD_(ULONG, Release)() { ULONG r = --m_ref; if (r == 0) delete this; return r; }
    STDMETHOD(QueryInterface)(REFIID iid, PVOID *out) {
        if (IsEqualIID(iid, __uuidof(IUnknown)) ||
            IsEqualIID(iid, __uuidof(IDebugOutputCallbacksWide))) {
            *out = static_cast<IDebugOutputCallbacksWide *>(this);
            AddRef(); return S_OK;
        }
        *out = NULL; return E_NOINTERFACE;
    }
    STDMETHOD(Output)(ULONG, PCWSTR) { return S_OK; }
};

int main(int argc, char **argv) {
    const char *conn = getenv("KDNET_CONN");
    if (!conn) { fprintf(stderr, "set KDNET_CONN\n"); return 1; }
    const char *dll_path = getenv("DBGENG_DLL");
    if (!dll_path) dll_path = "C:\\debuggers\\dbgeng.dll";

    /* CB_MODE controls which callbacks to install:
     *   none   - no callbacks (control: should block)
     *   event  - only event callbacks
     *   output - only output callbacks
     *   both   - both (default; mirrors gokd shim)
     */
    const char *cb_mode = getenv("CB_MODE");
    if (!cb_mode) cb_mode = "both";

    HMODULE h = LoadLibraryA(dll_path);
    if (!h) { printf("LoadLibrary FAILED %lu\n", GetLastError()); return 1; }
    PFN_DebugCreate pfnDebugCreate = (PFN_DebugCreate)GetProcAddress(h, "DebugCreate");

    IDebugClient5 *client = NULL;
    HRESULT hr = pfnDebugCreate(__uuidof(IDebugClient5), (void**)&client);
    printf("DebugCreate = 0x%08x\n", (unsigned)hr);
    if (FAILED(hr)) return 1;

    IDebugControl4 *control = NULL;
    client->QueryInterface(__uuidof(IDebugControl4), (void**)&control);

    StubEventCB *ev = NULL;
    StubOutputCB *out = NULL;
    if (strcmp(cb_mode, "event") == 0 || strcmp(cb_mode, "both") == 0) {
        ev = new StubEventCB();
        HRESULT r = client->SetEventCallbacksWide(ev);
        printf("SetEventCallbacksWide = 0x%08x\n", (unsigned)r);
    }
    if (strcmp(cb_mode, "output") == 0 || strcmp(cb_mode, "both") == 0) {
        out = new StubOutputCB();
        HRESULT r = client->SetOutputCallbacksWide(out);
        printf("SetOutputCallbacksWide = 0x%08x\n", (unsigned)r);
    }
    printf("CB_MODE = %s\n", cb_mode);

    control->AddEngineOptions(DEBUG_ENGOPT_INITIAL_BREAK);

    printf("AttachKernel...\n");
    hr = client->AttachKernel(DEBUG_ATTACH_KERNEL_CONNECTION, conn);
    printf("  = 0x%08x\n", (unsigned)hr);
    if (FAILED(hr)) return 1;

    printf("WaitForEvent(DEFAULT, INFINITE)...\n"); fflush(stdout);
    hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, INFINITE);
    printf("WaitForEvent = 0x%08x\n", (unsigned)hr);
    if (SUCCEEDED(hr)) {
        ULONG status = 0;
        control->GetExecutionStatus(&status);
        printf("ExecutionStatus = %lu\n", status);
    }

    client->EndSession(DEBUG_END_PASSIVE);
    control->Release();
    client->Release();
    return 0;
}
