#include <windows.h>
#include <objbase.h>
#include <dbgeng.h>
#include <cstdio>
#include <cstdlib>
#include <cstring>

typedef HRESULT (STDAPICALLTYPE *PFN_DebugCreate)(REFIID, PVOID*);

int main() {
    const char *conn = getenv("KDNET_CONN");
    if (!conn) { fprintf(stderr, "set KDNET_CONN\n"); return 1; }
    const char *dll_path = getenv("DBGENG_DLL");
    if (!dll_path) dll_path = "C:\\debuggers\\dbgeng.dll";
    ULONG timeout = 5000;
    if (const char *t = getenv("WAIT_TIMEOUT_MS")) timeout = strtoul(t, NULL, 10);
    bool do_qi = getenv("DO_QI") && strcmp(getenv("DO_QI"), "0") != 0;
    bool do_setint = getenv("DO_SETINT") && strcmp(getenv("DO_SETINT"), "0") != 0;
    bool infinite_first = getenv("INFINITE_FIRST") && strcmp(getenv("INFINITE_FIRST"), "0") != 0;
    printf("dll=%s timeout=%lu DO_QI=%d DO_SETINT=%d INFINITE_FIRST=%d\n", dll_path, timeout, do_qi, do_setint, infinite_first);

    HMODULE h = LoadLibraryA(dll_path);
    if (!h) { printf("LoadLibrary FAILED %lu\n", GetLastError()); return 1; }
    auto pfnDebugCreate = (PFN_DebugCreate)GetProcAddress(h, "DebugCreate");
    IDebugClient5 *client = NULL;
    HRESULT hr = pfnDebugCreate(__uuidof(IDebugClient5), (void**)&client);
    printf("DebugCreate = 0x%08x client=%p\n", (unsigned)hr, client);
    if (FAILED(hr)) return 1;
    IDebugControl4 *control = NULL;
    hr = client->QueryInterface(__uuidof(IDebugControl4), (void**)&control);
    printf("QI Control4 = 0x%08x control=%p\n", (unsigned)hr, control);
    if (FAILED(hr)) return 1;
    if (do_qi) {
        IDebugDataSpaces4 *data = NULL; IDebugSymbols3 *sym = NULL; IDebugRegisters2 *reg = NULL; IDebugSystemObjects4 *sys = NULL; IDebugAdvanced3 *adv = NULL;
        hr = client->QueryInterface(__uuidof(IDebugDataSpaces4), (void**)&data); printf("QI DataSpaces4 = 0x%08x %p\n", (unsigned)hr, data);
        hr = client->QueryInterface(__uuidof(IDebugSymbols3), (void**)&sym); printf("QI Symbols3 = 0x%08x %p\n", (unsigned)hr, sym);
        hr = client->QueryInterface(__uuidof(IDebugRegisters2), (void**)&reg); printf("QI Registers2 = 0x%08x %p\n", (unsigned)hr, reg);
        hr = client->QueryInterface(__uuidof(IDebugSystemObjects4), (void**)&sys); printf("QI SysObjects4 = 0x%08x %p\n", (unsigned)hr, sys);
        hr = client->QueryInterface(__uuidof(IDebugAdvanced3), (void**)&adv); printf("QI Advanced3 = 0x%08x %p\n", (unsigned)hr, adv);
        if (adv) adv->Release(); if (sys) sys->Release(); if (reg) reg->Release(); if (sym) sym->Release(); if (data) data->Release();
    }
    hr = control->AddEngineOptions(DEBUG_ENGOPT_INITIAL_BREAK); printf("AddEngineOptions = 0x%08x\n", (unsigned)hr);
    hr = client->AttachKernel(DEBUG_ATTACH_KERNEL_CONNECTION, conn); printf("AttachKernel = 0x%08x\n", (unsigned)hr); fflush(stdout);
    if (FAILED(hr)) return 1;
    if (do_setint) { hr = control->SetInterrupt(DEBUG_INTERRUPT_ACTIVE); printf("SetInterrupt = 0x%08x\n", (unsigned)hr); fflush(stdout); }
    if (infinite_first) {
        printf("WaitForEvent(INFINITE)...\n"); fflush(stdout);
        hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, INFINITE);
        printf("WaitForEvent(INFINITE) = 0x%08x\n", (unsigned)hr);
    } else {
        for (int i=0; i<20; i++) {
            DWORD tick = GetTickCount();
            hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, timeout);
            DWORD dt = GetTickCount() - tick;
            ULONG status = 0; control->GetExecutionStatus(&status);
            printf("WaitForEvent[%02d](%lu) = 0x%08x dt=%lu status=%lu\n", i, timeout, (unsigned)hr, dt, status); fflush(stdout);
            if (hr == S_OK) break;
            if (FAILED(hr) && hr != E_NOTIMPL) break;
        }
    }
    ULONG status=0; control->GetExecutionStatus(&status); printf("Final ExecutionStatus = %lu\n", status);
    client->EndSession(DEBUG_END_PASSIVE);
    control->Release(); client->Release();
    return 0;
}
