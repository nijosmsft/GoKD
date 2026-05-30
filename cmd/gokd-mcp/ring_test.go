package main

import (
	"testing"
)

func TestRingPushAndSince(t *testing.T) {
	r := newRing[int](4)

	if r.Len() != 0 {
		t.Fatalf("empty ring Len=%d, want 0", r.Len())
	}
	items, dropped := r.Since(0)
	if len(items) != 0 || dropped != 0 {
		t.Errorf("empty Since=(%v,%d), want (nil,0)", items, dropped)
	}

	for i := 1; i <= 3; i++ {
		r.Push(i)
	}
	items, dropped = r.Since(0)
	if dropped != 0 || len(items) != 3 {
		t.Fatalf("Since(0)=(%v,%d), want (3 items, 0 dropped)", items, dropped)
	}
	for i, v := range items {
		if v != i+1 {
			t.Errorf("items[%d]=%d, want %d", i, v, i+1)
		}
	}
}

func TestRingOverflow(t *testing.T) {
	r := newRing[int](4)
	for i := 1; i <= 10; i++ {
		r.Push(i)
	}
	if r.Len() != 4 {
		t.Errorf("Len=%d, want 4", r.Len())
	}
	items, dropped := r.Since(0)
	if dropped != 6 {
		t.Errorf("dropped=%d, want 6 (10 pushes, cap 4, caller token 0)", dropped)
	}
	if len(items) != 4 {
		t.Fatalf("len items=%d, want 4", len(items))
	}
	for i, v := range items {
		want := 7 + i // 7,8,9,10
		if v != want {
			t.Errorf("items[%d]=%d, want %d", i, v, want)
		}
	}
}

func TestRingSinceToken(t *testing.T) {
	r := newRing[int](8)
	var seqs []uint64
	for i := 1; i <= 5; i++ {
		seqs = append(seqs, r.Push(i*10))
	}
	// After 5 pushes, sequences are 0..4. Items 10..50.

	items, dropped := r.Since(seqs[2]) // get items strictly after seq 2 (item 30)
	if dropped != 0 {
		t.Errorf("dropped=%d, want 0", dropped)
	}
	if len(items) != 2 {
		t.Fatalf("len items=%d, want 2 (40, 50)", len(items))
	}
	if items[0] != 40 || items[1] != 50 {
		t.Errorf("items=%v, want [40 50]", items)
	}

	// SinceToken beyond the latest = empty.
	items, dropped = r.Since(seqs[4])
	if len(items) != 0 || dropped != 0 {
		t.Errorf("Since(latest)=(%v,%d), want (nil, 0)", items, dropped)
	}
}

func TestRingLast(t *testing.T) {
	r := newRing[string](3)
	if _, ok := r.Last(); ok {
		t.Errorf("empty ring Last returned ok=true")
	}
	r.Push("a")
	r.Push("b")
	v, ok := r.Last()
	if !ok || v != "b" {
		t.Errorf("Last=(%q,%v), want (b,true)", v, ok)
	}
	r.Push("c")
	r.Push("d") // evicts "a"
	v, ok = r.Last()
	if !ok || v != "d" {
		t.Errorf("Last=(%q,%v), want (d,true)", v, ok)
	}
}

func TestRingPushWith(t *testing.T) {
	type entry struct {
		seq uint64
		val string
	}
	r := newRing[entry](4)
	for i := 0; i < 3; i++ {
		r.PushWith(func(seq uint64) entry { return entry{seq: seq, val: "x"} })
	}
	items, _ := r.Since(0)
	if len(items) != 3 {
		t.Fatalf("len items=%d, want 3", len(items))
	}
	for i, e := range items {
		if e.seq != uint64(i+1) {
			t.Errorf("items[%d].seq=%d, want %d", i, e.seq, i+1)
		}
	}
}

func TestLRUCacheEviction(t *testing.T) {
	c := newLRUCache(3, 0)
	c.Put("a", []byte("1"))
	c.Put("b", []byte("2"))
	c.Put("c", []byte("3"))
	c.Put("d", []byte("4")) // evicts "a"

	if _, ok := c.Get("a"); ok {
		t.Errorf("expected 'a' to be evicted")
	}
	for _, k := range []string{"b", "c", "d"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("expected %q to be present", k)
		}
	}
	// After Get("b"), Get("c"), Get("d") in that order, d is newest, b
	// is oldest. Put("e") therefore evicts "b".
	c.Put("e", []byte("5"))
	if _, ok := c.Get("b"); ok {
		t.Errorf("expected 'b' to be evicted after Put('e')")
	}
	for _, k := range []string{"c", "d", "e"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("expected %q to still be present", k)
		}
	}
}

func TestLRUCacheByteCap(t *testing.T) {
	c := newLRUCache(100, 10)
	c.Put("a", make([]byte, 6))
	c.Put("b", make([]byte, 6)) // 12 bytes total, evicts "a"
	if _, ok := c.Get("a"); ok {
		t.Errorf("expected 'a' to be evicted by byte cap")
	}
	if _, ok := c.Get("b"); !ok {
		t.Errorf("expected 'b' to remain")
	}
}
