/*
 * test_kernel_resume.cpp — reattach to a paused kernel target, issue Go,
 * and detach cleanly. Useful when a prior INITIAL_BREAK debugger left the
 * target halted in the KDNET debug stub.
 *
 * Build:
 *   g++ -std=c++17 -O2 test_kernel_resume.cpp -o test_kernel_resume.exe -lole32 -luuid
 */
#include <windows.h>
#include <objbase.h>
#include <dbgeng.h>
#include <cstdio>
#include <cstdlib>

typedef HRESULT (STDAPICALLTYPE *PFN_DebugCreate)(REFIID, PVOID*);

int main() {
    const char *conn = getenv("KDNET_CONN");
    if (!conn) { fprintf(stderr, "set KDNET_CONN\n"); return 1; }
    const char *dll_path = getenv("DBGENG_DLL");
    if (!dll_path) dll_path = "C:\\debuggers\\dbgeng.dll";

    HMODULE h = LoadLibraryA(dll_path);
    if (!h) { printf("LoadLibrary FAILED %lu\n", GetLastError()); return 1; }
    PFN_DebugCreate pfnDebugCreate = (PFN_DebugCreate)GetProcAddress(h, "DebugCreate");

    IDebugClient5 *client = NULL;
    HRESULT hr = pfnDebugCreate(__uuidof(IDebugClient5), (void**)&client);
    if (FAILED(hr)) { printf("DebugCreate=0x%08x\n", (unsigned)hr); return 1; }

    IDebugControl4 *control = NULL;
    client->QueryInterface(__uuidof(IDebugControl4), (void**)&control);

    /* No INITIAL_BREAK — we want to attach silently and resume. */
    printf("AttachKernel...\n"); fflush(stdout);
    hr = client->AttachKernel(DEBUG_ATTACH_KERNEL_CONNECTION, conn);
    printf("  = 0x%08x\n", (unsigned)hr);
    if (FAILED(hr)) return 1;

    /* Wait briefly for the engine to acknowledge the target is at break. */
    printf("WaitForEvent(60s)...\n"); fflush(stdout);
    hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, 60000);
    printf("  = 0x%08x\n", (unsigned)hr);

    ULONG status = 0;
    control->GetExecutionStatus(&status);
    printf("ExecutionStatus before Go = %lu\n", status);

    /* Tell target to resume. */
    hr = control->SetExecutionStatus(DEBUG_STATUS_GO);
    printf("SetExecutionStatus(GO) = 0x%08x\n", (unsigned)hr);

    /* Issue one more WaitForEvent to flush the Go to target. Short timeout
     * — we don't want to actually block waiting for the next break. */
    hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, 2000);
    printf("WaitForEvent(2s) after Go = 0x%08x\n", (unsigned)hr);

    control->GetExecutionStatus(&status);
    printf("ExecutionStatus after Go = %lu\n", status);

    printf("EndSession(ACTIVE_DETACH)...\n");
    hr = client->EndSession(DEBUG_END_ACTIVE_DETACH);
    printf("  = 0x%08x\n", (unsigned)hr);

    control->Release();
    client->Release();
    return 0;
}
