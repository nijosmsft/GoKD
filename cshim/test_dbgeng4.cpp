/* Minimal test — try different CreateProcess flags and DEBUG_CREATE_PROCESS. */
#include <windows.h>
#include <dbgeng.h>
#include <cstdio>

int test(const char *label, ULONG create_flags) {
    HRESULT hr;

    IDebugClient *client = NULL;
    hr = DebugCreate(__uuidof(IDebugClient), (void**)&client);
    if (FAILED(hr)) return 1;

    IDebugControl *control = NULL;
    client->QueryInterface(__uuidof(IDebugControl), (void**)&control);

    /* Use CreateProcess2 which takes DEBUG_CREATE_PROCESS_OPTIONS. */
    char cmd[] = "C:\\Windows\\System32\\notepad.exe";
    printf("[%s] CreateProcess flags=0x%x\n", label, create_flags);
    hr = client->CreateProcess(0, cmd, create_flags);
    printf("[%s] CreateProcess: 0x%08x\n", label, (unsigned)hr);

    ULONG status = 0;
    control->GetExecutionStatus(&status);
    printf("[%s] Exec status: %lu\n", label, status);

    /* Try multiple short waits. */
    for (int i = 0; i < 5; i++) {
        hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, 1000);
        control->GetExecutionStatus(&status);
        printf("[%s] WaitForEvent attempt %d: hr=0x%08x status=%lu\n",
               label, i, (unsigned)hr, status);
        if (hr == S_OK || status == DEBUG_STATUS_BREAK) break;
    }

    if (status == DEBUG_STATUS_BREAK) {
        printf("[%s] SUCCESS — target is broken in!\n", label);

        IDebugSymbols *sym = NULL;
        if (SUCCEEDED(client->QueryInterface(__uuidof(IDebugSymbols), (void**)&sym))) {
            ULONG loaded = 0, unloaded = 0;
            sym->GetNumberModules(&loaded, &unloaded);
            printf("[%s] modules: loaded=%lu unloaded=%lu\n", label, loaded, unloaded);
            sym->Release();
        }
    } else {
        printf("[%s] FAILED — not broken in (status=%lu)\n", label, status);
    }

    client->EndSession(DEBUG_END_ACTIVE_TERMINATE);
    control->Release();
    client->Release();
    return 0;
}

int main() {
    CoInitializeEx(NULL, COINIT_MULTITHREADED);

    test("DEBUG_PROCESS", DEBUG_PROCESS);
    printf("\n");
    test("DEBUG_ONLY_THIS_PROCESS", DEBUG_ONLY_THIS_PROCESS);
    printf("\n");
    test("BOTH", DEBUG_PROCESS | DEBUG_ONLY_THIS_PROCESS);

    CoUninitialize();
    return 0;
}
