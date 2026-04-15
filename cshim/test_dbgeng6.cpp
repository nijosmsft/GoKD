/* Force SDK dbgeng.dll via SetDllDirectory + LoadLibrary. */
#include <windows.h>
#include <dbgeng.h>
#include <cstdio>

typedef HRESULT (STDAPICALLTYPE *PFN_DebugCreate)(REFIID, PVOID*);

int main() {
    CoInitializeEx(NULL, COINIT_MULTITHREADED);

    /* Force DLL search to SDK debugger tools first. */
    const char *dbgdir = "C:\\Program Files (x86)\\Windows Kits\\10\\Debuggers\\x64";
    SetDllDirectoryA(dbgdir);

    HMODULE hmod = LoadLibraryA("dbgeng.dll");
    if (!hmod) {
        printf("LoadLibrary failed: %lu\n", GetLastError());
        return 1;
    }

    char path[512];
    GetModuleFileNameA(hmod, path, sizeof(path));
    printf("Loaded dbgeng.dll from: %s\n", path);

    PFN_DebugCreate pfn = (PFN_DebugCreate)GetProcAddress(hmod, "DebugCreate");
    if (!pfn) {
        printf("DebugCreate not found\n");
        return 1;
    }

    IDebugClient *client = NULL;
    HRESULT hr = pfn(__uuidof(IDebugClient), (void**)&client);
    printf("DebugCreate: 0x%08x\n", (unsigned)hr);

    IDebugControl *control = NULL;
    client->QueryInterface(__uuidof(IDebugControl), (void**)&control);

    IDebugSymbols *symbols = NULL;
    client->QueryInterface(__uuidof(IDebugSymbols), (void**)&symbols);

    char cmd[] = "C:\\Windows\\System32\\notepad.exe";
    hr = client->CreateProcessAndAttach(0, cmd,
        DEBUG_ONLY_THIS_PROCESS, 0, DEBUG_ATTACH_DEFAULT);
    printf("CreateProcessAndAttach: 0x%08x\n", (unsigned)hr);

    for (int i = 0; i < 10; i++) {
        hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, 2000);
        ULONG status = 0;
        control->GetExecutionStatus(&status);
        ULONG loaded = 0, unloaded = 0;
        symbols->GetNumberModules(&loaded, &unloaded);
        printf("attempt %d: WFE=0x%08x status=%lu modules=%lu\n",
               i, (unsigned)hr, status, loaded);
        if (loaded > 0) {
            printf("SUCCESS!\n");
            for (ULONG j = 0; j < loaded && j < 5; j++) {
                ULONG64 base = 0;
                symbols->GetModuleByIndex(j, &base);
                char img[256]={}, mod[256]={}, li[256]={};
                symbols->GetModuleNames(j, base, img, 256, NULL, mod, 256, NULL, li, 256, NULL);
                printf("  [%lu] %s @ 0x%llx\n", j, mod, base);
            }
            break;
        }
    }

    client->EndSession(DEBUG_END_ACTIVE_TERMINATE);
    symbols->Release();
    control->Release();
    client->Release();
    CoUninitialize();
    return 0;
}
