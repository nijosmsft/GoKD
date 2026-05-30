package gokd

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

// TypeValue is one node in a recursive type walk. Name is empty for the
// root. Raw is the bytes covering this value's address; nil if the read
// failed (Err records why). Children is populated for struct/union nodes
// when MaxDepth > 0. Decoded is set by the per-type special decoders for
// _UNICODE_STRING (string), _LIST_ENTRY (struct{Flink, Blink uint64}),
// GUID (string), and _LARGE_INTEGER (int64).
type TypeValue struct {
	Name     string
	TypeName string
	Address  uint64
	Size     uint32
	Raw      []byte
	Err      error
	Children []*TypeValue
	Decoded  any
}

// DumpTypeOptions configures Session.DumpType.
type DumpTypeOptions struct {
	// MaxDepth bounds recursion. 0 means "header only" (no children).
	// Default applied by DumpType: 3.
	MaxDepth int
	// FollowPtrs, when true, follows non-NULL pointer fields one level
	// deeper. Implemented as a single-level dereference per pointer to
	// keep the walk bounded; cycle detection guarantees termination.
	FollowPtrs bool
}

// ListEntry is the decoded form of an _LIST_ENTRY field.
type ListEntry struct {
	Flink uint64
	Blink uint64
}

// errCycle is the sentinel set on TypeValue.Err when a pointer leads
// back to an address already visited at a shallower depth.
var errCycle = errors.New("cycle detected")

// DumpType walks a typed object at addr inside module's symbol
// namespace, recursing up to opts.MaxDepth levels (default 3). The
// returned tree carries raw bytes per node plus DbgEng's reported field
// types. Use opts.FollowPtrs to follow non-NULL pointer fields one
// additional level; cycle detection guarantees termination.
//
// Source-level limitations:
//   - Bitfields are not exposed by IDebugSymbols3::GetFieldTypeAndOffset;
//     they appear with Size==0 and Raw==nil (Err records "bitfield").
//   - Anonymous unions/structs may surface as overlapping fields at the
//     same offset — DbgEng's flat field list is reproduced verbatim.
//   - Public PDBs alone usually do not include user-defined type info;
//     walks against types like _PEB require either private PDBs or the
//     OS's published types.
func (s *session) DumpType(ctx context.Context, module, typeName string, addr uint64, opts DumpTypeOptions) (*TypeValue, error) {
	if opts.MaxDepth == 0 {
		opts.MaxDepth = 3
	}
	if opts.MaxDepth < 0 {
		opts.MaxDepth = 0
	}
	visited := make(map[uint64]int)
	return s.dumpTypeRec(ctx, module, typeName, addr, opts.MaxDepth, opts.FollowPtrs, visited)
}

func (s *session) dumpTypeRec(ctx context.Context, module, typeName string, addr uint64, depth int, followPtrs bool, visited map[uint64]int) (*TypeValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	size, err := s.TypeSize(module, typeName)
	if err != nil {
		return nil, err
	}

	tv := &TypeValue{
		Name:     "",
		TypeName: typeName,
		Address:  addr,
		Size:     uint32(size),
	}

	if size > 0 && addr != 0 {
		raw, readErr := s.ReadMemory(addr, size)
		if readErr != nil {
			tv.Err = readErr
		} else {
			tv.Raw = raw
		}
	}

	tv.Decoded = decodeKnownType(typeName, tv.Raw, s, addr)

	visited[addr] = depth

	if depth <= 0 {
		return tv, nil
	}

	fields, err := s.TypeFields(module, typeName)
	if err != nil {
		// Not a struct (e.g. primitive). Just return the leaf.
		return tv, nil
	}

	for _, f := range fields {
		if err := ctx.Err(); err != nil {
			return tv, err
		}
		child := s.walkField(ctx, module, f, addr, depth-1, followPtrs, visited)
		tv.Children = append(tv.Children, child)
	}

	return tv, nil
}

func (s *session) walkField(ctx context.Context, module string, f *Field, parentAddr uint64, depth int, followPtrs bool, visited map[uint64]int) *TypeValue {
	child := &TypeValue{
		Name:     f.Name,
		TypeName: f.TypeName,
		Address:  parentAddr + uint64(f.Offset),
		Size:     uint32(f.Size),
	}

	if f.Size == 0 {
		child.Err = errors.New("bitfield or zero-size field; raw bytes not available")
		return child
	}

	raw, readErr := s.ReadMemory(child.Address, f.Size)
	if readErr != nil {
		child.Err = readErr
	} else {
		child.Raw = raw
	}

	child.Decoded = decodeKnownType(f.TypeName, child.Raw, s, child.Address)

	if depth <= 0 {
		return child
	}

	// Recurse into nested structs (skip primitives by trying TypeFields).
	if !isPointerType(f.TypeName) {
		if sub, err := s.TypeFields(module, f.TypeName); err == nil && len(sub) > 0 {
			for _, ff := range sub {
				if err := ctx.Err(); err != nil {
					return child
				}
				gc := s.walkField(ctx, module, ff, child.Address, depth-1, followPtrs, visited)
				child.Children = append(child.Children, gc)
			}
			return child
		}
	}

	// Pointer follow.
	if followPtrs && isPointerType(f.TypeName) && len(child.Raw) >= 8 {
		ptr := binary.LittleEndian.Uint64(child.Raw[:8])
		if ptr == 0 {
			return child
		}
		if prev, seen := visited[ptr]; seen && prev > depth {
			child.Err = errCycle
			return child
		}
		baseType := strings.TrimSpace(strings.TrimSuffix(f.TypeName, "*"))
		baseType = strings.TrimSpace(baseType)
		if baseType == "" {
			return child
		}
		sub, err := s.dumpTypeRec(ctx, module, baseType, ptr, depth, followPtrs, visited)
		if err != nil {
			child.Err = err
			return child
		}
		// Hang the dereferenced subtree under a synthetic child so
		// callers can tell the difference between in-place fields and
		// followed pointers.
		sub.Name = "*"
		child.Children = append(child.Children, sub)
	}

	return child
}

// isPointerType reports whether a DbgHelp-reported type name names a
// pointer. DbgEng surfaces pointer types with a trailing "*" (often
// with a space, e.g. "_PEB *") or as "Ptr32"/"Ptr64".
func isPointerType(name string) bool {
	n := strings.TrimSpace(name)
	if n == "" {
		return false
	}
	if strings.HasSuffix(n, "*") {
		return true
	}
	if n == "Ptr32" || n == "Ptr64" {
		return true
	}
	return false
}

// decodeKnownType applies the per-type special decoders described in
// the t1-7 design. Returns nil when the type is not one of the
// recognised cases (or the bytes are too short to decode).
func decodeKnownType(typeName string, raw []byte, s *session, _ uint64) any {
	n := strings.TrimSpace(typeName)
	// Strip leading underscore so _UNICODE_STRING and UNICODE_STRING
	// both match. Case is preserved because DbgEng is consistent.
	bare := strings.TrimPrefix(n, "_")

	switch strings.ToUpper(bare) {
	case "UNICODE_STRING":
		// USHORT Length; USHORT MaximumLength; PWSTR Buffer;
		// On x64: 2 + 2 + 4 (pad) + 8 = 16 bytes.
		if len(raw) < 16 {
			return nil
		}
		length := binary.LittleEndian.Uint16(raw[0:2])
		buffer := binary.LittleEndian.Uint64(raw[8:16])
		if length == 0 || buffer == 0 {
			return ""
		}
		bytes, err := s.ReadMemory(buffer, uint64(length))
		if err != nil {
			return nil
		}
		u16 := make([]uint16, len(bytes)/2)
		for i := range u16 {
			u16[i] = binary.LittleEndian.Uint16(bytes[i*2 : i*2+2])
		}
		return string(utf16.Decode(u16))

	case "LIST_ENTRY":
		if len(raw) < 16 {
			return nil
		}
		return ListEntry{
			Flink: binary.LittleEndian.Uint64(raw[0:8]),
			Blink: binary.LittleEndian.Uint64(raw[8:16]),
		}

	case "GUID":
		if len(raw) < 16 {
			return nil
		}
		d1 := binary.LittleEndian.Uint32(raw[0:4])
		d2 := binary.LittleEndian.Uint16(raw[4:6])
		d3 := binary.LittleEndian.Uint16(raw[6:8])
		return fmt.Sprintf("%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x",
			d1, d2, d3,
			raw[8], raw[9],
			raw[10], raw[11], raw[12], raw[13], raw[14], raw[15])

	case "LARGE_INTEGER", "ULARGE_INTEGER":
		if len(raw) < 8 {
			return nil
		}
		v := int64(binary.LittleEndian.Uint64(raw[0:8]))
		return v
	}
	return nil
}
