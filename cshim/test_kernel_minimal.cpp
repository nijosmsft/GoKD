/*
 * Minimal standalone test: AttachKernel + WaitForEvent(INFINITE).
 * Tries to reproduce gokd's behavior in the smallest possible standalone
 * program, with no callbacks, no Go, no goroutines, no shim machinery.
 *
 * Build (in MSYS2 MinGW64):
 *   g++ -std=c++17 -O2 test_kernel_minimal.cpp -o test_kernel_minimal.exe -lole32 -luuid
 *
 * Run with:
 *   $env:KDNET_CONN = 'net:port=50000,key=...'
 *   .\test_kernel_minimal.exe
 *
 * If this prints WaitForEvent: 0x80004001 then the bug is in something
 * common to gokd shim AND this minimal program (likely MinGW vtable or
 * dbgeng load path). If it prints S_OK, the bug is in gokd's surrounding
 * code (callbacks, COM init, threading, etc.).
 */

#include <windows.h>
#include <objbase.h>
#include <dbgeng.h>
#include <cstdio>
#include <cstdlib>

typedef HRESULT (STDAPICALLTYPE *PFN_DebugCreate)(REFIID, PVOID*);

int main(int argc, char **argv) {
    const char *conn = getenv("KDNET_CONN");
    if (!conn) {
        fprintf(stderr, "set KDNET_CONN env var\n");
        return 1;
    }

    /* Default: load C:\debuggers\dbgeng.dll explicitly so we use the
     * same DLL gokd uses. */
    const char *dll_path = getenv("DBGENG_DLL");
    if (!dll_path) dll_path = "C:\\debuggers\\dbgeng.dll";

    /* Default: no CoInit (set NO_COINIT=1 to skip; set COINIT=mta or
     * COINIT=sta to enable specific mode). */
    const char *coinit = getenv("COINIT");
    if (coinit && coinit[0]) {
        DWORD mode = COINIT_MULTITHREADED;
        if (strcmp(coinit, "sta") == 0) mode = COINIT_APARTMENTTHREADED;
        HRESULT cohr = CoInitializeEx(NULL, mode);
        printf("CoInitializeEx(%s) = 0x%08x\n", coinit, (unsigned)cohr);
    } else {
        printf("(no CoInit)\n");
    }

    printf("LoadLibraryA('%s')...\n", dll_path);
    HMODULE h = LoadLibraryA(dll_path);
    if (!h) {
        printf("  FAILED: GetLastError=%lu\n", GetLastError());
        return 1;
    }
    printf("  OK h=%p\n", h);

    PFN_DebugCreate pfnDebugCreate = (PFN_DebugCreate)GetProcAddress(h, "DebugCreate");
    if (!pfnDebugCreate) {
        printf("GetProcAddress(DebugCreate) FAILED\n");
        return 1;
    }

    IDebugClient5 *client = NULL;
    HRESULT hr = pfnDebugCreate(__uuidof(IDebugClient5), (void**)&client);
    printf("DebugCreate(IDebugClient5) = 0x%08x client=%p\n", (unsigned)hr, client);
    if (FAILED(hr)) return 1;

    IDebugControl4 *control = NULL;
    hr = client->QueryInterface(__uuidof(IDebugControl4), (void**)&control);
    printf("QI(IDebugControl4) = 0x%08x control=%p\n", (unsigned)hr, control);
    if (FAILED(hr)) return 1;

    hr = control->AddEngineOptions(DEBUG_ENGOPT_INITIAL_BREAK);
    printf("AddEngineOptions(INITIAL_BREAK) = 0x%08x\n", (unsigned)hr);

    printf("AttachKernel(CONNECTION, '%s')...\n", conn);
    hr = client->AttachKernel(DEBUG_ATTACH_KERNEL_CONNECTION, conn);
    printf("  = 0x%08x\n", (unsigned)hr);
    if (FAILED(hr)) return 1;

    /* Single INFINITE WaitForEvent — no loop, no polling. */
    printf("WaitForEvent(DEFAULT, INFINITE)... fflush\n");
    fflush(stdout);
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
