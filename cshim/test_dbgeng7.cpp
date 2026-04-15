/* Test WITHOUT CoInitializeEx — like CDB does it. */
#include <windows.h>
#include <dbgeng.h>
#include <cstdio>

int main() {
    /* No CoInitializeEx — CDB doesn't use it. */

    IDebugClient *client = NULL;
    HRESULT hr = DebugCreate(__uuidof(IDebugClient), (void**)&client);
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

    printf("WaitForEvent(INFINITE)...\n");
    fflush(stdout);
    hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, INFINITE);
    printf("WaitForEvent: 0x%08x\n", (unsigned)hr);

    ULONG status = 0;
    control->GetExecutionStatus(&status);
    printf("Exec status: %lu\n", status);

    ULONG loaded = 0, unloaded = 0;
    hr = symbols->GetNumberModules(&loaded, &unloaded);
    printf("Modules: loaded=%lu unloaded=%lu hr=0x%08x\n",
           loaded, unloaded, (unsigned)hr);

    if (loaded > 0) {
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
    return 0;
}
