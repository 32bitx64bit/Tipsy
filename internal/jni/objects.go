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
	id      int64
	class   *Class
	str     string
	fields  map[string]any
	elems   []int64
	bytes   []byte
	global  bool
	direct  unsafe.Pointer
	cap     int64
	arrKind int // 0 object, else primitive kind rune
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
	vm.mu.Lock()
	defer vm.mu.Unlock()
	return vm.objects[id]
}

func (vm *VM) newObjectLocked(cls *Class) *Object {
	o := &Object{id: vm.allocID(), class: cls, fields: make(map[string]any)}
	vm.put(o)
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
