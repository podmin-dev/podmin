// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dataplane

// Generate the committed little- and big-endian eBPF objects with bpf2go v0.22.0.
// Run this command in a Linux container providing clang-14, llvm-strip-14, and libbpf headers.
//go:generate go tool bpf2go -cc clang-14 -strip llvm-strip-14 -cflags "-O2 -g -Wall -Werror" bpf dataplane.bpf.c -- -I/usr/include -I/usr/include/x86_64-linux-gnu
