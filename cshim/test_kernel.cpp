/* Test kernel attach — compile and run from SDK dir. */
#include <windows.h>
#include <dbgeng.h>
#include <cstdio>

int main() {
    HRESULT hr;

    IDebugClient5 *client = NULL;
    hr = DebugCreate(__uuidof(IDebugClient5), (void**)&client);
    printf("DebugCreate(IDebugClient5): 0x%08x\n", (unsigned)hr);
    if (FAILED(hr)) return 1;

    IDebugControl4 *control = NULL;
    client->QueryInterface(__uuidof(IDebugControl4), (void**)&control);
    control->AddEngineOptions(DEBUG_ENGOPT_INITIAL_BREAK);

    const char *conn = "net:port=50000,key=142799hwammrg.nylkha8kqstm.pj3hb6zx2l4c.pyuywxtolc8m,target=10.57.201.67";

    printf("AttachKernel (ANSI) conn='%s'...\n", conn);
    hr = client->AttachKernel(DEBUG_ATTACH_KERNEL_CONNECTION, conn);
    printf("AttachKernel: 0x%08x\n", (unsigned)hr);

    if (hr == E_NOTIMPL) {
        printf("\nTrying via base IDebugClient...\n");
        IDebugClient *base = NULL;
        client->QueryInterface(__uuidof(IDebugClient), (void**)&base);
        if (base) {
            hr = base->AttachKernel(DEBUG_ATTACH_KERNEL_CONNECTION, conn);
            printf("base->AttachKernel: 0x%08x\n", (unsigned)hr);
            base->Release();
        }
    }

    if (SUCCEEDED(hr)) {
        printf("Waiting for kernel connection (5s)...\n");
        hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, 5000);
        printf("WaitForEvent: 0x%08x\n", (unsigned)hr);
    }

    client->EndSession(DEBUG_END_PASSIVE);
    if (control) control->Release();
    client->Release();
    return 0;
}
