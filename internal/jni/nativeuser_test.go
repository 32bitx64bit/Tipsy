// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"testing"
)

const fakeNativeUserLoginJSON = `{"userId":1,"isUnder13":false,"username":"tester","displayName":"tester-display","membershipType":3,"hasRobloxSubscription":true,"countryCode":"ZZTESTLAND"}`

func resetNativeUserLogForTest() {
	ResetNativeUserForTest()
}

func plantNativeUserLogin(t *testing.T, vm *VM, payload string) {
	t.Helper()
	vm.mu.Lock()
	cls := vm.ensureClassLocked(nativeHelperClass)
	h := vm.newObjectLocked(cls)
	s := vm.newStringLocked(payload)
	vm.mu.Unlock()
	v, handled := vm.dispatch(idToJobject(h.id), nativeHelperClass, "gameActivity_onDidLogInReceived", "(Ljava/lang/String;)V", testPackObjectArg(s.id))
	if !handled {
		t.Fatal("gameActivity_onDidLogInReceived not handled")
	}
	if uintptr(v) != uintptr(idToJobject(h.id)) {
		t.Fatalf("void dispatch return = %#x, want the receiver", uintptr(v))
	}
}

func allocNativeUser(t *testing.T, vm *VM) uintptr {
	t.Helper()
	env := vm.Env()
	cls := env.FindClass(nativeUserClass)
	if cls == 0 {
		t.Fatal("NativeUserJavaInterface class missing")
	}
	obj := env.AllocObject(cls)
	if obj == 0 {
		t.Fatal("AllocObject NativeUserJavaInterface failed")
	}
	return obj
}

func nativeUserRecv(obj uintptr) uintptr {
	return uintptr(idToJobject(jobjectToID(obj)))
}

func nativeUserString(t *testing.T, vm *VM, recv uintptr, name string) string {
	t.Helper()
	v, handled := callDispatchOrStub(vm, idToJobject(jobjectToID(recv)), nativeUserClass, name, "()Ljava/lang/String;", nil, 'L')
	if !handled {
		t.Fatalf("%s not handled", name)
	}
	got, err := vm.Env().GetStringUTFChars(uintptr(v))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestNativeUserPlatformNameIsWindowsSpoof(t *testing.T) {
	if nativeUserPlatformName != "Windows" {
		t.Fatalf("getPlatformName = %q, want Enum.Platform.Windows spoof", nativeUserPlatformName)
	}

	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetNativeUserLogForTest()
	t.Cleanup(ResetNativeUserForTest)
	resetStubDispatchForTest()
	buf := captureLogs(t)
	obj := allocNativeUser(t, vm)
	recv := nativeUserRecv(obj)

	v, handled := callDispatchOrStub(vm, idToJobject(jobjectToID(recv)), nativeUserClass, "getPlatformName", "()Ljava/lang/String;", nil, 'L')
	if !handled {
		t.Fatal("getPlatformName not handled")
	}
	got, err := vm.Env().GetStringUTFChars(uintptr(v))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Windows" {
		t.Fatalf("getPlatformName = %q, want Windows", got)
	}

	out := buf.String()
	if strings.Contains(out, "stub-dispatch") {
		t.Fatalf("stub-dispatch fired: %s", out)
	}
	if strings.Contains(out, "Windows") {
		t.Fatalf("product string leaked into log: %s", out)
	}
	if !strings.Contains(out, "method=getPlatformName") || !strings.Contains(out, "spoof=pc") {
		t.Fatalf("missing pc spoof record: %s", out)
	}
}

func TestNativeUserGettersEmptyBeforeLogin(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetNativeUserLogForTest()
	t.Cleanup(ResetNativeUserForTest)
	resetStubDispatchForTest()
	buf := captureLogs(t)
	recv := nativeUserRecv(allocNativeUser(t, vm))

	if nativeUserString(t, vm, recv, "getUsername") != "" {
		t.Fatal("getUsername before login want empty")
	}
	if nativeUserString(t, vm, recv, "getDisplayName") != "" {
		t.Fatal("getDisplayName before login want empty")
	}
	if nativeUserString(t, vm, recv, "getAlternateName") != "" {
		t.Fatal("getAlternateName before login want empty")
	}
	if nativeUserString(t, vm, recv, "getTheme") != "" {
		t.Fatal("getTheme before login want empty")
	}
	if nativeUserString(t, vm, recv, "getPlatformName") != "Windows" {
		t.Fatal("getPlatformName before login want Windows")
	}

	v, handled := callDispatchOrStub(vm, idToJobject(jobjectToID(recv)), nativeUserClass, "getUserId", "()J", nil, 'J')
	if !handled {
		t.Fatal("getUserId not handled before login")
	}
	if int64(uintptr(v)) != 0 {
		t.Fatalf("getUserId before login = %d, want 0", uintptr(v))
	}
	v, handled = callDispatchOrStub(vm, idToJobject(jobjectToID(recv)), nativeUserClass, "getIsUnder13", "()Z", nil, 'Z')
	if !handled {
		t.Fatal("getIsUnder13 not handled before login")
	}
	if uintptr(v) != 0 {
		t.Fatalf("getIsUnder13 before login = %d, want false", uintptr(v))
	}
	v, handled = callDispatchOrStub(vm, idToJobject(jobjectToID(recv)), nativeUserClass, "getMembershipType", "()I", nil, 'I')
	if !handled {
		t.Fatal("getMembershipType not handled before login")
	}
	if int32(uintptr(v)) != 0 {
		t.Fatalf("getMembershipType before login = %d, want 0", uintptr(v))
	}
	v, handled = callDispatchOrStub(vm, idToJobject(jobjectToID(recv)), nativeUserClass, "getHasRobloxSubscription", "()Z", nil, 'Z')
	if !handled {
		t.Fatal("getHasRobloxSubscription not handled before login")
	}
	if uintptr(v) != 0 {
		t.Fatalf("getHasRobloxSubscription before login = %d, want false", uintptr(v))
	}

	out := buf.String()
	if strings.Contains(out, "stub-dispatch") {
		t.Fatalf("stub-dispatch fired for handled getters: %s", out)
	}
}

func TestNativeUserGettersMatchPlantedLoginJSON(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetNativeUserLogForTest()
	t.Cleanup(ResetNativeUserForTest)
	resetStubDispatchForTest()
	recv := nativeUserRecv(allocNativeUser(t, vm))
	plantNativeUserLogin(t, vm, fakeNativeUserLoginJSON)

	if nativeUserString(t, vm, recv, "getUsername") != "tester" {
		t.Fatalf("getUsername = %q, want tester", nativeUserString(t, vm, recv, "getUsername"))
	}
	if nativeUserString(t, vm, recv, "getDisplayName") != "tester-display" {
		t.Fatalf("getDisplayName = %q, want tester-display", nativeUserString(t, vm, recv, "getDisplayName"))
	}
	if nativeUserString(t, vm, recv, "getAlternateName") != "" {
		t.Fatalf("getAlternateName = %q, want empty when absent", nativeUserString(t, vm, recv, "getAlternateName"))
	}
	if nativeUserString(t, vm, recv, "getTheme") != "" {
		t.Fatalf("getTheme = %q, want empty when absent", nativeUserString(t, vm, recv, "getTheme"))
	}
	if nativeUserString(t, vm, recv, "getPlatformName") != "Windows" {
		t.Fatal("getPlatformName after login want Windows")
	}

	v, handled := callDispatchOrStub(vm, idToJobject(jobjectToID(recv)), nativeUserClass, "getUserId", "()J", nil, 'J')
	if !handled {
		t.Fatal("getUserId not handled after login")
	}
	if int64(uintptr(v)) != 1 {
		t.Fatalf("getUserId = %d, want 1", uintptr(v))
	}
	v, handled = callDispatchOrStub(vm, idToJobject(jobjectToID(recv)), nativeUserClass, "getIsUnder13", "()Z", nil, 'Z')
	if !handled {
		t.Fatal("getIsUnder13 not handled after login")
	}
	if uintptr(v) != 0 {
		t.Fatalf("getIsUnder13 = %d, want false from payload", uintptr(v))
	}
	v, handled = callDispatchOrStub(vm, idToJobject(jobjectToID(recv)), nativeUserClass, "getMembershipType", "()I", nil, 'I')
	if !handled {
		t.Fatal("getMembershipType not handled after login")
	}
	if int32(uintptr(v)) != 3 {
		t.Fatalf("getMembershipType = %d, want 3", uintptr(v))
	}
	v, handled = callDispatchOrStub(vm, idToJobject(jobjectToID(recv)), nativeUserClass, "getHasRobloxSubscription", "()Z", nil, 'Z')
	if !handled {
		t.Fatal("getHasRobloxSubscription not handled after login")
	}
	if uintptr(v) == 0 {
		t.Fatal("getHasRobloxSubscription want true from payload")
	}
}

func TestNativeUserIsUnder13FollowsPayload(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetNativeUserLogForTest()
	t.Cleanup(ResetNativeUserForTest)
	recv := nativeUserRecv(allocNativeUser(t, vm))
	plantNativeUserLogin(t, vm, `{"userId":1,"isUnder13":true,"username":"tester"}`)
	v, handled := callDispatchOrStub(vm, idToJobject(jobjectToID(recv)), nativeUserClass, "getIsUnder13", "()Z", nil, 'Z')
	if !handled {
		t.Fatal("getIsUnder13 not handled")
	}
	if uintptr(v) == 0 {
		t.Fatal("getIsUnder13 want true from payload; must not force-false")
	}
}

func TestNativeUserOptionalFieldsWhenPresent(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetNativeUserLogForTest()
	t.Cleanup(ResetNativeUserForTest)
	recv := nativeUserRecv(allocNativeUser(t, vm))
	plantNativeUserLogin(t, vm, `{"userId":1,"username":"tester","alternateName":"alt-tester","theme":"Dark"}`)
	if nativeUserString(t, vm, recv, "getAlternateName") != "alt-tester" {
		t.Fatalf("getAlternateName = %q, want alt-tester", nativeUserString(t, vm, recv, "getAlternateName"))
	}
	if nativeUserString(t, vm, recv, "getTheme") != "Dark" {
		t.Fatalf("getTheme = %q, want Dark", nativeUserString(t, vm, recv, "getTheme"))
	}
}

func TestNativeUserLoginDoesNotLogIdentity(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetNativeUserLogForTest()
	t.Cleanup(ResetNativeUserForTest)
	buf := captureLogs(t)
	plantNativeUserLogin(t, vm, fakeNativeUserLoginJSON)
	_ = nativeUserString(t, vm, nativeUserRecv(allocNativeUser(t, vm)), "getUsername")

	out := buf.String()
	if !strings.Contains(out, "[jni] native-user snapshot") {
		t.Fatalf("missing snapshot log: %s", out)
	}
	if !strings.Contains(out, "hasUserId=true") || !strings.Contains(out, "under13=false") || !strings.Contains(out, "membershipType=3") {
		t.Fatalf("snapshot log missing allowed aggregates: %s", out)
	}
	for _, leak := range []string{"tester", "tester-display", "ZZTESTLAND", fakeNativeUserLoginJSON, "userId=1", `"username"`} {
		if strings.Contains(out, leak) {
			t.Fatalf("identity leaked into log %q: %s", leak, out)
		}
	}
}

func TestNativeUserWrongClassFallsThrough(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetStubDispatchForTest()
	if _, handled := callDispatchOrStub(vm, jnull(), "java/io/File", "getPlatformName", "()Ljava/lang/String;", nil, 'L'); handled {
		t.Fatal("non-NativeUser class handled by NativeUser dispatch")
	}
	if _, handled := callDispatchOrStub(vm, jnull(), nativeUserClass, "getPlatformName", "()I", nil, 'I'); handled {
		t.Fatal("wrong sig handled")
	}
	if _, handled := callDispatchOrStub(vm, jnull(), nativeUserClass, "getUserId", "()I", nil, 'I'); handled {
		t.Fatal("wrong getUserId sig handled")
	}
}

func TestNativeUserPlatformNameIsImplemented(t *testing.T) {
	pairs := [][2]string{
		{"getPlatformName", "()Ljava/lang/String;"},
		{"getUserId", "()J"},
		{"getIsUnder13", "()Z"},
		{"getUsername", "()Ljava/lang/String;"},
		{"getDisplayName", "()Ljava/lang/String;"},
		{"getAlternateName", "()Ljava/lang/String;"},
		{"getMembershipType", "()I"},
		{"getHasRobloxSubscription", "()Z"},
		{"getTheme", "()Ljava/lang/String;"},
	}
	for _, p := range pairs {
		if !isImplementedMethod(p[0], p[1]) {
			t.Fatalf("%s%s missing from implementedMethods", p[0], p[1])
		}
	}
}

func TestNativeUserParseLoginJSON(t *testing.T) {
	snap, ok := parseNativeUserLoginJSON(fakeNativeUserLoginJSON)
	if !ok {
		t.Fatal("valid DID_LOG_IN JSON rejected")
	}
	if !snap.HasUserID || snap.UserID != 1 || snap.Username != "tester" || snap.DisplayName != "tester-display" {
		t.Fatalf("parsed snapshot = %+v", snap)
	}
	if snap.IsUnder13 || snap.MembershipType != 3 || !snap.HasRobloxSubscription {
		t.Fatalf("parsed flags = %+v", snap)
	}
	if snap.AlternateName != "" || snap.Theme != "" {
		t.Fatalf("absent optional fields = %+v", snap)
	}

	if _, ok := parseNativeUserLoginJSON(""); ok {
		t.Fatal("empty JSON applied")
	}
	if _, ok := parseNativeUserLoginJSON("   "); ok {
		t.Fatal("whitespace JSON applied")
	}
	if _, ok := parseNativeUserLoginJSON("{"); ok {
		t.Fatal("truncated JSON applied")
	}
	if _, ok := parseNativeUserLoginJSON("[]"); ok {
		t.Fatal("array JSON applied")
	}
	if _, ok := parseNativeUserLoginJSON("null"); ok {
		t.Fatal("null JSON applied")
	}
	if _, ok := parseNativeUserLoginJSON(`{"countryCode":"ZZ"}`); ok {
		t.Fatal("unknown-keys-only object applied")
	}
	if _, ok := parseNativeUserLoginJSON(`not-json`); ok {
		t.Fatal("malformed JSON applied")
	}

	snap, ok = parseNativeUserLoginJSON(`{"userId":1.0,"isUnder13":true}`)
	if !ok || snap.UserID != 1 || !snap.IsUnder13 {
		t.Fatalf("numeric/bool payload = %+v ok=%v", snap, ok)
	}
}
