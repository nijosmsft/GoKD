/* Test with explicit path and process tracking. */
#include <windows.h>
#include <dbgeng.h>
#include <cstdio>
#include <tlhelp32.h>

void list_processes(const char *name) {
    HANDLE snap = CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
    PROCESSENTRY32 pe = {sizeof(pe)};
    if (Process32First(snap, &pe)) {
        do {
            if (strstr(pe.szExeFile, name))
                printf("  found: %s pid=%lu\n", pe.szExeFile, pe.th32ProcessID);
        } while (Process32Next(snap, &pe));
    }
    CloseHandle(snap);
}

int main() {
    HRESULT hr;
    hr = CoInitializeEx(NULL, COINIT_MULTITHREADED);

    IDebugClient *client = NULL;
    hr = DebugCreate(__uuidof(IDebugClient), (void**)&client);
    printf("DebugCreate: 0x%08x\n", (unsigned)hr);

    IDebugControl *control = NULL;
    client->QueryInterface(__uuidof(IDebugControl), (void**)&control);

    printf("Before CreateProcess, notepad procs:\n");
    list_processes("notepad");

    /* Try with full path. */
    char cmd[] = "C:\\Windows\\System32\\notepad.exe";
    printf("\nCreateProcessAndAttach('%s')...\n", cmd);
    hr = client->CreateProcessAndAttach(0, cmd,
        DEBUG_ONLY_THIS_PROCESS, 0, DEBUG_ATTACH_DEFAULT);
    printf("Result: 0x%08x\n", (unsigned)hr);

    printf("\nAfter CreateProcess, notepad procs:\n");
    list_processes("notepad");

    ULONG status = 0;
    control->GetExecutionStatus(&status);
    printf("\nExec status before WaitForEvent: %lu\n", status);

    printf("WaitForEvent(3000)...\n");
    hr = control->WaitForEvent(DEBUG_WAIT_DEFAULT, 3000);
    printf("WaitForEvent: 0x%08x\n", (unsigned)hr);

    control->GetExecutionStatus(&status);
    printf("Exec status after: %lu\n", status);

    /* Try again without the attach part. */
    printf("\n--- Trying CreateProcess (no attach) ---\n");
    client->EndSession(DEBUG_END_ACTIVE_TERMINATE);

    IDebugClient *client2 = NULL;
    DebugCreate(__uuidof(IDebugClient), (void**)&client2);
    IDebugControl *control2 = NULL;
    client2->QueryInterface(__uuidof(IDebugControl), (void**)&control2);

    char cmd2[] = "C:\\Windows\\System32\\notepad.exe";
    hr = client2->CreateProcess(0, cmd2, DEBUG_ONLY_THIS_PROCESS);
    printf("CreateProcess: 0x%08x\n", (unsigned)hr);

    control2->GetExecutionStatus(&status);
    printf("Exec status: %lu\n", status);

    hr = control2->WaitForEvent(DEBUG_WAIT_DEFAULT, 3000);
    printf("WaitForEvent: 0x%08x\n", (unsigned)hr);

    control2->GetExecutionStatus(&status);
    printf("Exec status after: %lu\n", status);

    printf("\nNotepad procs:\n");
    list_processes("notepad");

    client2->EndSession(DEBUG_END_ACTIVE_TERMINATE);
    control2->Release();
    client2->Release();
    control->Release();
    client->Release();
    CoUninitialize();
    return 0;
}
