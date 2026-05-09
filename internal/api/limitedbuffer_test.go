package api

import (
	"strings"
	"testing"
)

func TestLimitedBufferTruncatesAtLimit(t *testing.T) {
	lb := &limitedBuffer{limit: 10}
	n, err := lb.Write([]byte("hello world!")) // 12 bytes, limit 10
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 12 {
		t.Errorf("Write should report all bytes consumed, got %d", n)
	}
	if lb.Len() != 10 {
		t.Errorf("expected buffer length 10, got %d", lb.Len())
	}
	if lb.String() != "hello worl" {
		t.Errorf("expected %q, got %q", "hello worl", lb.String())
	}
}

func TestLimitedBufferDiscardsAfterFull(t *testing.T) {
	lb := &limitedBuffer{limit: 5}
	lb.Write([]byte("12345"))
	n, err := lb.Write([]byte("more"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 4 {
		t.Errorf("Write should report all bytes consumed, got %d", n)
	}
	if lb.Len() != 5 {
		t.Errorf("expected buffer length 5, got %d", lb.Len())
	}
	if lb.String() != "12345" {
		t.Errorf("expected %q, got %q", "12345", lb.String())
	}
}

func TestLimitedBufferSmallWrites(t *testing.T) {
	lb := &limitedBuffer{limit: 10}
	for i := 0; i < 20; i++ {
		lb.Write([]byte("x"))
	}
	if lb.Len() != 10 {
		t.Errorf("expected buffer length 10, got %d", lb.Len())
	}
	if lb.String() != strings.Repeat("x", 10) {
		t.Errorf("expected 10 x's, got %q", lb.String())
	}
}

func TestLimitedBufferUnderLimit(t *testing.T) {
	lb := &limitedBuffer{limit: 100}
	lb.Write([]byte("short"))
	if lb.Len() != 5 {
		t.Errorf("expected buffer length 5, got %d", lb.Len())
	}
	if lb.String() != "short" {
		t.Errorf("expected %q, got %q", "short", lb.String())
	}
}
