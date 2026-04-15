/* Test with STA apartment. */
#include <windows.h>
#include <objbase.h>
#include <dbgeng.h>
#include <cstdio>

int main() {
    /* Try STA apartment. */
    HRESULT hr = CoInitializeEx(NULL, COINIT_APARTMENTTHREADED);
    printf("CoInitializeEx(STA): 0x%08x\n", (unsigned)hr);

    IDebugClient *client = NULL;
    hr = DebugCreate(__uuidof(IDebugClient), (void**)&client);
    printf("DebugCreate: 0x%08x\n", (unsigned)hr);
    if (FAILED(hr)) return 1;

    IDebugControl *control = NULL;
    client->QueryInterface(__uuidof(IDebugControl), (void**)&control);

    IDebugSymbols *symbols = NULL;
    client->QueryInterface(__uuidof(IDebugSymbols), (void**)&symbols);

    char cmd[] = "C:\\Windows\\System32\\notepad.exe";
    hr = client->CreateProcessAndAttach(0, cmd,
        DEBUG_ONLY_THIS_PROCESS, 0, DEBUG_ATTACH_DEFAULT);
    printf("CreateProcessAndAttach: 0x%08x\n", (unsigned)hr);
    if (FAILED(hr)) return 1;

    printf("WaitForEvent(10000)...\n");
    fflush(stdout);
    hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, 10000);
    printf("WaitForEvent: 0x%08x\n", (unsigned)hr);

    ULONG status = 0;
    control->GetExecutionStatus(&status);
    printf("Exec status: %lu\n", status);

    ULONG loaded = 0, unloaded = 0;
    hr = symbols->GetNumberModules(&loaded, &unloaded);
    printf("Modules: loaded=%lu hr=0x%08x\n", loaded, (unsigned)hr);

    if (loaded > 0) {
        printf("SUCCESS! Modules:\n");
        for (ULONG i = 0; i < loaded && i < 5; i++) {
            ULONG64 base = 0;
            symbols->GetModuleByIndex(i, &base);
            char img[256]={}, mod[256]={}, li[256]={};
            symbols->GetModuleNames(i, base, img, 256, NULL,
                mod, 256, NULL, li, 256, NULL);
            printf("  [%lu] %s @ 0x%llx\n", i, mod, base);
        }
    }

    client->EndSession(DEBUG_END_ACTIVE_TERMINATE);
    symbols->Release();
    control->Release();
    client->Release();
    CoUninitialize();
    return 0;
}
