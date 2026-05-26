# GoKD C++ shim Makefile
#
# Builds libgokd_shim.a — the static library that CGo links against.
#
# Prerequisites:
#   - MinGW-w64 (MSYS2: pacman -S mingw-w64-x86_64-gcc)
#   - Windows SDK / Debugging Tools for Windows
#
# Set WINDBG_SDK to the Debugging Tools directory, e.g.:
#   export WINDBG_SDK="/c/Program Files (x86)/Windows Kits/10/Debuggers"
# Modern Windows SDK installs put headers at $(WINDBG_SDK)/inc.
# Legacy standalone Debugging Tools SDKs put them at $(WINDBG_SDK)/sdk/inc
# (in which case WINDBG_SDK typically ends in /x64). Both layouts are probed.
#
# Usage:
#   make           # build lib/libgokd_shim.a
#   make clean     # remove build artefacts

CXX      ?= g++
AR       ?= ar
CXXFLAGS  = -std=c++17 -Wall -Wextra -Wno-unused-parameter -O2

# Windows SDK include paths for dbgeng.h / dbghelp.h.
# These are typically found in the Debugging Tools SDK or the Windows SDK.
# Probe both modern (Debuggers/inc) and legacy (Debuggers/x64/sdk/inc) layouts;
# g++ silently ignores -I directories that do not exist.
ifdef WINDBG_SDK
CXXFLAGS += -I"$(WINDBG_SDK)/inc"
CXXFLAGS += -I"$(WINDBG_SDK)/sdk/inc"
endif

# Also check the standard Windows Kit include path.
ifdef WINDOWS_SDK_INCLUDE
CXXFLAGS += -I"$(WINDOWS_SDK_INCLUDE)/um" -I"$(WINDOWS_SDK_INCLUDE)/shared"
endif

SRCS = dispatch_thread.cpp callbacks.cpp gokd_shim.cpp
OBJS = $(SRCS:.cpp=.o)

.PHONY: all clean

all: lib/libgokd_shim.a

lib/libgokd_shim.a: $(OBJS) | lib
	$(AR) rcs $@ $(OBJS)

lib:
	mkdir -p lib

%.o: %.cpp gokd_shim.h
	$(CXX) $(CXXFLAGS) -c $< -o $@

clean:
	rm -f $(OBJS) lib/libgokd_shim.a
