// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import "unsafe"

type Class struct {
	name  string
	super *Class
	obj   *Object
}

type Object struct {
	id        int64
	class     *Class
	str       string
	fields    map[string]any
	elems     []int64
	bytes     []byte
	global    bool
	immortal  bool // never reclaimed; class objects, interned caches, seeded enums
	heapRefs  int  // Tipsy-heap edges (fields/elems), not JNI global refs
	heapEdges []int64
	direct    unsafe.Pointer
	cap       int64
	arrKind   int // 0 object, else primitive kind rune
}

func (o *Object) markImmortal() {
	if o == nil {
		return
	}
	o.global = true
	o.immortal = true
}

func (vm *VM) allocID() int64 {
	id := vm.nextID
	vm.nextID++
	return id
}

func (vm *VM) put(o *Object) {
	if o.fields == nil {
		o.fields = make(map[string]any)
	}
	vm.objects[o.id] = o
}

func (vm *VM) get(id int64) *Object {
	if id == 0 {
		return nil
	}
	vm.mu.RLock()
	o := vm.objects[id]
	vm.mu.RUnlock()
	return o
}

func (vm *VM) newObjectLocked(cls *Class) *Object {
	o := &Object{id: vm.allocID(), class: cls, fields: make(map[string]any)}
	vm.put(o)
	vm.addLocalLocked(o.id)
	return o
}

func (vm *VM) newStringLocked(s string) *Object {
	cls := vm.classes["java/lang/String"]
	o := vm.newObjectLocked(cls)
	o.str = s
	return o
}

func (vm *VM) newFileLocked(path string) *Object {
	cls := vm.classes["java/io/File"]
	o := vm.newObjectLocked(cls)
	o.str = path
	o.fields["path"] = path
	return o
}

func (cls *Class) isAssignable(from *Class) bool {
	for c := from; c != nil; c = c.super {
		if c == cls || (c != nil && cls != nil && c.name == cls.name) {
			return true
		}
	}
	return false
}

func (vm *VM) cachedStringLocked(slot **Object, value string) *Object {
	if *slot != nil && (*slot).str == value {
		vm.addLocalLocked((*slot).id)
		return *slot
	}
	o := vm.newStringLocked(value)
	o.markImmortal()
	*slot = o
	return o
}

func (vm *VM) cachedFileLocked(slot **Object, path string) *Object {
	if *slot != nil && (*slot).str == path {
		vm.addLocalLocked((*slot).id)
		return *slot
	}
	o := vm.newFileLocked(path)
	o.markImmortal()
	*slot = o
	return o
}
