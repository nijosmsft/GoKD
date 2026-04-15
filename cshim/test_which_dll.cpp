#include <windows.h>
#include <dbgeng.h>
#include <cstdio>

int main() {
    HMODULE h = GetModuleHandleA("dbgeng.dll");
    if (h) {
        char path[512];
        GetModuleFileNameA(h, path, sizeof(path));
        printf("dbgeng.dll already loaded from: %s\n", path);
    } else {
        printf("dbgeng.dll not yet loaded\n");
    }

    IDebugClient *c = NULL;
    HRESULT hr = DebugCreate(__uuidof(IDebugClient), (void**)&c);
    printf("DebugCreate: 0x%08x\n", (unsigned)hr);

    h = GetModuleHandleA("dbgeng.dll");
    if (h) {
        char path[512];
        GetModuleFileNameA(h, path, sizeof(path));
        printf("dbgeng.dll now loaded from: %s\n", path);
    }

    if (c) {
        hr = c->AttachKernel(0, "net:port=1,key=a.b.c.d");
        printf("AttachKernel test: 0x%08x\n", (unsigned)hr);
        c->Release();
    }
    return 0;
}
