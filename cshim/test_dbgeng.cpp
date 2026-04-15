/* Minimal standalone test for DbgEng CreateProcessAndAttach. */
#include <windows.h>
#include <dbgeng.h>
#include <cstdio>

int main() {
    HRESULT hr;

    hr = CoInitializeEx(NULL, COINIT_MULTITHREADED);
    printf("CoInitializeEx: 0x%08x\n", (unsigned)hr);

    IDebugClient *client = NULL;
    hr = DebugCreate(__uuidof(IDebugClient), (void**)&client);
    printf("DebugCreate: 0x%08x\n", (unsigned)hr);
    if (FAILED(hr)) return 1;

    IDebugControl *control = NULL;
    hr = client->QueryInterface(__uuidof(IDebugControl), (void**)&control);
    printf("QI IDebugControl: 0x%08x\n", (unsigned)hr);
    if (FAILED(hr)) return 1;

    char cmd[] = "notepad.exe";
    printf("Calling CreateProcessAndAttach...\n");
    hr = client->CreateProcessAndAttach(0, cmd,
        DEBUG_ONLY_THIS_PROCESS, 0, DEBUG_ATTACH_DEFAULT);
    printf("CreateProcessAndAttach: 0x%08x\n", (unsigned)hr);
    if (FAILED(hr)) return 1;

    printf("Calling WaitForEvent(5000)...\n");
    hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, 5000);
    printf("WaitForEvent: 0x%08x\n", (unsigned)hr);

    ULONG status = 0;
    control->GetExecutionStatus(&status);
    printf("Execution status: %lu\n", status);

    /* Try to get number of modules. */
    IDebugSymbols *symbols = NULL;
    hr = client->QueryInterface(__uuidof(IDebugSymbols), (void**)&symbols);
    if (SUCCEEDED(hr)) {
        ULONG loaded = 0, unloaded = 0;
        hr = symbols->GetNumberModules(&loaded, &unloaded);
        printf("GetNumberModules: 0x%08x loaded=%lu unloaded=%lu\n",
               (unsigned)hr, loaded, unloaded);
        symbols->Release();
    }

    client->EndSession(DEBUG_END_ACTIVE_TERMINATE);
    control->Release();
    client->Release();
    CoUninitialize();
    return 0;
}
