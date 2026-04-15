/* Mimic CDB as closely as possible. */
#include <windows.h>
#include <objbase.h>
#include <dbgeng.h>
#include <cstdio>

int main() {
    /* CDB sets initial breakpoint via engine options. */
    IDebugClient5 *client = NULL;
    HRESULT hr = DebugCreate(__uuidof(IDebugClient5), (void**)&client);
    printf("DebugCreate(IDebugClient5): 0x%08x\n", (unsigned)hr);
    if (FAILED(hr)) return 1;

    IDebugControl4 *control = NULL;
    client->QueryInterface(__uuidof(IDebugControl4), (void**)&control);

    IDebugSymbols3 *symbols = NULL;
    client->QueryInterface(__uuidof(IDebugSymbols3), (void**)&symbols);

    /* Set engine options like CDB does — enable initial break. */
    ULONG opts = 0;
    control->GetEngineOptions(&opts);
    printf("Engine options before: 0x%08x\n", opts);
    control->AddEngineOptions(DEBUG_ENGOPT_INITIAL_BREAK);
    control->GetEngineOptions(&opts);
    printf("Engine options after:  0x%08x\n", opts);

    char cmda[] = "C:\\Windows\\System32\\notepad.exe";
    hr = client->CreateProcessAndAttach(0, cmda,
        DEBUG_ONLY_THIS_PROCESS, 0, DEBUG_ATTACH_DEFAULT);
    printf("CreateProcessAndAttach: 0x%08x\n", (unsigned)hr);

    ULONG status = 0;
    control->GetExecutionStatus(&status);
    printf("Exec status before WFE: %lu\n", status);

    printf("WaitForEvent(10000)...\n");
    fflush(stdout);
    hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, 10000);
    printf("WaitForEvent: 0x%08x\n", (unsigned)hr);

    control->GetExecutionStatus(&status);
    printf("Exec status after: %lu\n", status);

    ULONG loaded = 0, unloaded = 0;
    hr = symbols->GetNumberModules(&loaded, &unloaded);
    printf("Modules: loaded=%lu hr=0x%08x\n", loaded, (unsigned)hr);

    if (loaded > 0) {
        printf("SUCCESS!\n");
        for (ULONG i = 0; i < loaded && i < 10; i++) {
            ULONG64 base = 0;
            symbols->GetModuleByIndex(i, &base);
            wchar_t mod[256] = {};
            symbols->GetModuleNameStringWide(DEBUG_MODNAME_MODULE, i, base,
                mod, 256, NULL);
            printf("  [%lu] %S @ 0x%llx\n", i, mod, base);
        }
    } else {
        printf("No modules — trying Go + WaitForEvent cycle...\n");
        for (int cycle = 0; cycle < 5; cycle++) {
            control->SetExecutionStatus(DEBUG_STATUS_GO);
            hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, 3000);
            control->GetExecutionStatus(&status);
            symbols->GetNumberModules(&loaded, &unloaded);
            printf("  cycle %d: WFE=0x%08x status=%lu modules=%lu\n",
                   cycle, (unsigned)hr, status, loaded);
            if (loaded > 0) break;
        }
    }

    client->EndSession(DEBUG_END_ACTIVE_TERMINATE);
    symbols->Release();
    control->Release();
    client->Release();
    return 0;
}
