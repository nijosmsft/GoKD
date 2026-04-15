/* Test with WinDbg's dbgeng.dll loaded explicitly. */
#include <windows.h>
#include <dbgeng.h>
#include <cstdio>

typedef HRESULT (STDAPICALLTYPE *PFN_DebugCreate)(REFIID, PVOID*);

int main() {
    HRESULT hr;
    hr = CoInitializeEx(NULL, COINIT_MULTITHREADED);
    printf("CoInitializeEx: 0x%08x\n", (unsigned)hr);

    /* Try loading WinDbg's dbgeng.dll explicitly. */
    const char *paths[] = {
        "C:\\Program Files (x86)\\Windows Kits\\10\\Debuggers\\x64\\dbgeng.dll",
        "C:\\Program Files\\WindowsApps\\Microsoft.WinDbg_1.2603.20001.0_x64__8wekyb3d8bbwe\\amd64\\dbgeng.dll",
        NULL
    };

    HMODULE hmod = NULL;
    for (int i = 0; paths[i]; i++) {
        hmod = LoadLibraryA(paths[i]);
        if (hmod) {
            printf("Loaded dbgeng from: %s\n", paths[i]);
            break;
        }
    }
    if (!hmod) {
        hmod = LoadLibraryA("dbgeng.dll");
        printf("Loaded system dbgeng.dll\n");
    }
    if (!hmod) {
        printf("Failed to load dbgeng.dll\n");
        return 1;
    }

    PFN_DebugCreate pfn = (PFN_DebugCreate)GetProcAddress(hmod, "DebugCreate");
    if (!pfn) {
        printf("DebugCreate not found\n");
        return 1;
    }

    IDebugClient *client = NULL;
    hr = pfn(__uuidof(IDebugClient), (void**)&client);
    printf("DebugCreate: 0x%08x\n", (unsigned)hr);
    if (FAILED(hr)) return 1;

    IDebugControl *control = NULL;
    hr = client->QueryInterface(__uuidof(IDebugControl), (void**)&control);
    printf("QI IDebugControl: 0x%08x\n", (unsigned)hr);

    char cmd[] = "notepad.exe";
    printf("CreateProcessAndAttach...\n");
    hr = client->CreateProcessAndAttach(0, cmd,
        DEBUG_ONLY_THIS_PROCESS, 0, DEBUG_ATTACH_DEFAULT);
    printf("Result: 0x%08x\n", (unsigned)hr);

    printf("WaitForEvent(5000)...\n");
    hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, 5000);
    printf("Result: 0x%08x\n", (unsigned)hr);

    ULONG status = 0;
    control->GetExecutionStatus(&status);
    printf("Exec status: %lu\n", status);

    IDebugSymbols *symbols = NULL;
    if (SUCCEEDED(client->QueryInterface(__uuidof(IDebugSymbols), (void**)&symbols))) {
        ULONG loaded = 0, unloaded = 0;
        hr = symbols->GetNumberModules(&loaded, &unloaded);
        printf("Modules: loaded=%lu unloaded=%lu (hr=0x%08x)\n",
               loaded, unloaded, (unsigned)hr);
        symbols->Release();
    }

    client->EndSession(DEBUG_END_ACTIVE_TERMINATE);
    control->Release();
    client->Release();
    CoUninitialize();
    return 0;
}
