/*
 * gokd_internal.h — Internal header shared between C++ shim files.
 * NOT exposed to CGo.
 */

#pragma once

#include <windows.h>
#undef CreateProcess

#include <dbgeng.h>
#include <dbghelp.h>

#include "gokd_shim.h"

/* Session state — all COM interfaces and callback registrations. */
struct gokd_session {
    IDebugClient5        *client;
    IDebugControl4       *control;
    IDebugDataSpaces4    *data_spaces;
    IDebugSymbols3       *symbols;
    IDebugRegisters2     *registers;
    IDebugSystemObjects4 *sys_objects;
    IDebugAdvanced3      *advanced;

    /* Our callback implementations (C++ classes). */
    IDebugEventCallbacksWide  *event_cbs_impl;
    IDebugOutputCallbacksWide *output_cbs_impl;

    /* User-registered Go callbacks. */
    gokd_event_fn   event_fn;
    void            *event_ctx;
    gokd_output_fn  output_fn;
    void            *output_ctx;

    /* Last stop event captured by callbacks during WaitForEvent. */
    gokd_stop_event_t last_stop;

    /* Most recent HRESULT from a failed call. */
    int32_t last_error;

    /* Whether COM was initialised by us on this thread. */
    int com_initialised;
};

/* Get the session pointer from an opaque handle. */
gokd_session *gokd_get_session(gokd_session_t handle);

/* UTF-8 ↔ UTF-16 helpers. */
wchar_t *utf8_to_wide(const char *utf8);
int wide_to_utf8(const wchar_t *wide, char *out, size_t out_len);
void wide_to_utf8_fixed(const wchar_t *wide, char *out, size_t out_size);

/* Callback factory functions (callbacks.cpp). */
IDebugEventCallbacksWide *gokd_create_event_callbacks(gokd_session *s);
IDebugOutputCallbacksWide *gokd_create_output_callbacks(gokd_session *s);
void gokd_destroy_event_callbacks(IDebugEventCallbacksWide *cbs);
void gokd_destroy_output_callbacks(IDebugOutputCallbacksWide *cbs);
