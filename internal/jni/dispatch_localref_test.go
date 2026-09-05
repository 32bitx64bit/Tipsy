// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"sync/atomic"
	"testing"
)

func TestCallACachesHandlerOnSecondInvoke(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := internMethod("tipsy/test/HandlerCache", "getAllocatableBytes", "()J", false)
	info, ok := lookupMethod(mid)
	if !ok {
		t.Fatal("interned method missing")
	}
	if info.loadHandler() != nil {
		t.Fatal("handler bound before first CallA")
	}

	out := vm.callA(0, mid, 1, 'J')
	if out.j != 8<<30 {
		t.Fatalf("first CallA getAllocatableBytes = %d", out.j)
	}
	if info.loadHandler() == nil {
		t.Fatal("first CallA must store a handler on the interned method")
	}

	var hits atomic.Int32
	info.wrapHandlerHits(&hits)

	out = vm.callA(0, mid, 1, 'J')
	if hits.Load() != 1 {
		t.Fatalf("second CallA handler hits = %d, want 1", hits.Load())
	}
	if out.j != 8<<30 {
		t.Fatalf("second CallA getAllocatableBytes = %d", out.j)
	}
}

func TestCallACachedStubStillDedupsDiagnostic(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetStubDispatchForTest()
	buf := captureLogs(t)
	mid, _ := internMethod("tipsy/test/CallAStubCache", "mystery", "(I)V", true)
	vm.callA(0, mid, 1, 'V')
	vm.callA(0, mid, 1, 'V')
	if got := strings.Count(buf.String(), "stub-dispatch"); got != 1 {
		t.Fatalf("CallA stub diagnostics = %d, want 1: %s", got, buf.String())
	}
	info, _ := lookupMethod(mid)
	if info.loadHandler() == nil {
		t.Fatal("stub identity must still cache a handler")
	}
}

func allocLocalObject(vm *VM) int64 {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	o := vm.newObjectLocked(vm.classes["java/lang/Object"])
	return o.id
}

func TestCallACachesInitHandler(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	id := allocLocalObject(vm)
	mid, _ := internMethod("java/lang/Object", "<init>", "()V", false)
	vm.callA(id, mid, 0, 'V')
	info, _ := lookupMethod(mid)
	if info.loadHandler() == nil {
		t.Fatal("<init> must cache a handler")
	}
	if vm.get(id) == nil {
		t.Fatal("<init> must not reclaim the receiver")
	}
}

func TestDeleteLocalRefRemovesNonGlobal(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	id := allocLocalObject(vm)
	if vm.get(id) == nil {
		t.Fatal("AllocObject missing from map")
	}
	vm.deleteLocal(id)
	if vm.get(id) != nil {
		t.Fatal("DeleteLocalRef must reclaim a non-global object")
	}
}

func TestPopLocalFrameReclaimsLocalsKeepsResult(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	if vm.pushFrame(16) != 0 {
		t.Fatal("PushLocalFrame")
	}
	keepID := allocLocalObject(vm)
	dropID := allocLocalObject(vm)
	got := vm.popFrame(keepID)
	if got != keepID {
		t.Fatal("PopLocalFrame must return the result handle")
	}
	if vm.get(keepID) == nil {
		t.Fatal("PopLocalFrame must keep the result")
	}
	if vm.get(dropID) != nil {
		t.Fatal("PopLocalFrame must reclaim other frame locals")
	}
}

func TestImmortalPackageNameSurvivesDeleteLocalRef(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	a, ok := vm.dispatch(jnull(), "android/content/Context", "getPackageName", "()Ljava/lang/String;", nil)
	if !ok || jobjectToID(uintptr(a)) == 0 {
		t.Fatal("getPackageName")
	}
	id := jobjectToID(uintptr(a))
	vm.deleteLocal(id)
	if vm.get(id) == nil {
		t.Fatal("immortal getPackageName must survive DeleteLocalRef")
	}
	b, ok := vm.dispatch(jnull(), "android/content/Context", "getPackageName", "()Ljava/lang/String;", nil)
	if !ok || b != a {
		t.Fatal("getPackageName must still reuse the immortal String")
	}
}

func TestNewGlobalRefSurvivesPopLocalFrame(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	if vm.pushFrame(16) != 0 {
		t.Fatal("PushLocalFrame")
	}
	id := allocLocalObject(vm)
	if vm.newGlobal(id) != id {
		t.Fatal("NewGlobalRef must keep the same handle")
	}
	vm.popFrame(0)
	if vm.get(id) == nil {
		t.Fatal("NewGlobalRef must survive PopLocalFrame of the creating frame")
	}
	vm.deleteGlobal(id)
	if vm.get(id) != nil {
		t.Fatal("DeleteGlobalRef must reclaim a non-immortal object with no remaining locals")
	}
}

func TestNewLocalRefSurvivesOneDelete(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	id := allocLocalObject(vm)
	if vm.newLocal(id) != id {
		t.Fatal("NewLocalRef must return the same handle")
	}
	vm.deleteLocal(id)
	if vm.get(id) == nil {
		t.Fatal("object must remain while a second local ref exists")
	}
	vm.deleteLocal(id)
	if vm.get(id) != nil {
		t.Fatal("object must be reclaimed after the last local ref")
	}
}

func TestDeleteLocalRefDoesNotReclaimClassObjects(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	cls := vm.classes["java/lang/String"]
	if cls == nil || cls.obj == nil {
		t.Fatal("String class missing")
	}
	id := cls.obj.id
	vm.deleteLocal(id)
	if vm.get(id) == nil {
		t.Fatal("class objects are immortal")
	}
}

func TestCallAHandlerBindRace(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := internMethod("tipsy/test/HandlerRace", "getAllocatableBytes", "()J", false)
	const n = 32
	var fails atomic.Int32
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			out := vm.callA(0, mid, 1, 'J')
			if out.j != 8<<30 {
				fails.Add(1)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	if fails.Load() != 0 {
		t.Fatalf("racy CallA mismatches = %d", fails.Load())
	}
	info, _ := lookupMethod(mid)
	if info.loadHandler() == nil {
		t.Fatal("handler missing after concurrent CallA")
	}
}

func TestNewLocalRefOfReclaimedIsNull(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	id := allocLocalObject(vm)
	vm.deleteLocal(id)
	if vm.newLocal(id) != 0 {
		t.Fatal("NewLocalRef of a reclaimed id must be null")
	}
}

func TestHeapPinKeepsPathStringAfterDeleteLocalRef(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	f := vm.newFileLocked("/tmp/tipsy-heap-pin")
	fid := f.id
	vm.mu.Unlock()
	s, ok := vm.dispatch(idToJobject(fid), "java/io/File", "getAbsolutePath", "()Ljava/lang/String;", nil)
	if !ok {
		t.Fatal("getAbsolutePath")
	}
	sid := jobjectToID(uintptr(s))
	if sid == 0 || vm.get(sid) == nil {
		t.Fatal("path String missing")
	}
	vm.deleteLocal(sid)
	if vm.get(sid) == nil {
		t.Fatal("File-held path String must survive DeleteLocalRef")
	}
	s2, ok := vm.dispatch(idToJobject(fid), "java/io/File", "getAbsolutePath", "()Ljava/lang/String;", nil)
	if !ok || jobjectToID(uintptr(s2)) != sid {
		t.Fatal("getAbsolutePath must reuse the heap-pinned String")
	}
}

func TestHeapPinKeepsArrayElementUntilListReclaimed(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	list := vm.newObjectLocked(vm.ensureClassLocked("java/util/ArrayList"))
	listID := list.id
	elem := vm.newObjectLocked(vm.classes["java/lang/Object"])
	elemID := elem.id
	vm.replaceHeapEdgeLocked(list, 0, elemID)
	list.elems = append(list.elems, elemID)
	vm.mu.Unlock()
	vm.deleteLocal(elemID)
	if vm.get(elemID) == nil {
		t.Fatal("list-held element must survive DeleteLocalRef")
	}
	vm.deleteLocal(listID)
	if vm.get(elemID) != nil {
		t.Fatal("element must reclaim after the holding list is gone")
	}
}
