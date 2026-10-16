package extract

/*
#include <stddef.h>
#include <stdlib.h>

extern void ts_set_allocator(void *(*new_malloc)(size_t), void *(*new_calloc)(size_t, size_t), void *(*new_realloc)(void *, size_t), void (*new_free)(void *));

static void use_libc_allocator(void) { ts_set_allocator(malloc, calloc, realloc, free); }
*/
import "C"

// The tree-sitter Go bindings route every allocation of the C parser through a Go callback, which dominates the time of
// a large index. The parser's memory is plain C memory, so the C library allocator does the same job without the
// crossing between the two runtimes.
func init() { C.use_libc_allocator() }
