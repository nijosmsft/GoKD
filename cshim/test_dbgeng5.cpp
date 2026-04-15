/* Loop WaitForEvent until we get modules. */
#include <windows.h>
#include <dbgeng.h>
#include <cstdio>

int main() {
    CoInitializeEx(NULL, COINIT_MULTITHREADED);

    IDebugClient *client = NULL;
    DebugCreate(__uuidof(IDebugClient), (void**)&client);

    IDebugControl *control = NULL;
    client->QueryInterface(__uuidof(IDebugControl), (void**)&control);

    IDebugSymbols *symbols = NULL;
    client->QueryInterface(__uuidof(IDebugSymbols), (void**)&symbols);

    char cmd[] = "C:\\Windows\\System32\\notepad.exe";
    HRESULT hr = client->CreateProcessAndAttach(0, cmd,
        DEBUG_ONLY_THIS_PROCESS, 0, DEBUG_ATTACH_DEFAULT);
    printf("CreateProcessAndAttach: 0x%08x\n", (unsigned)hr);

    for (int i = 0; i < 30; i++) {
        hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, 1000);
        ULONG status = 0;
        control->GetExecutionStatus(&status);

        ULONG loaded = 0, unloaded = 0;
        HRESULT mhr = symbols->GetNumberModules(&loaded, &unloaded);

        printf("attempt %2d: WFE=0x%08x status=%lu modules=%lu (mhr=0x%08x)\n",
               i, (unsigned)hr, status, loaded, (unsigned)mhr);

        if (loaded > 0 && status == DEBUG_STATUS_BREAK) {
            printf("\nSUCCESS: %lu modules loaded, target is broken in.\n", loaded);

            /* Print module names. */
            for (ULONG j = 0; j < loaded && j < 10; j++) {
                ULONG64 base = 0;
                symbols->GetModuleByIndex(j, &base);
                char name[256] = {};
                char image[256] = {}, module[256] = {}, loaded_image[256] = {};
                symbols->GetModuleNames(j, base, image, sizeof(image), NULL,
                    module, sizeof(module), NULL,
                    loaded_image, sizeof(loaded_image), NULL);
                printf("  [%lu] 0x%016llx %s\n", j, base, module);
            }
            break;
        }

        if (status == DEBUG_STATUS_NO_DEBUGGEE) {
            printf("  -> no debuggee, retrying...\n");
        }

        /* If target is running, set interrupt to break. */
        if (status == DEBUG_STATUS_GO || status == DEBUG_STATUS_STEP_INTO) {
            control->SetInterrupt(DEBUG_INTERRUPT_ACTIVE);
        }
    }

    client->EndSession(DEBUG_END_ACTIVE_TERMINATE);
    symbols->Release();
    control->Release();
    client->Release();
    CoUninitialize();
    return 0;
}
