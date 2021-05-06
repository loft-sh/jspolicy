package vm

import (
	"gotest.tools/assert"
	"testing"
	"time"
)

func TestSimple(t *testing.T) {
	printed := []string{}
	vm, err := NewVM(nil, nil, func(str string) {
		printed = append(printed, str)
	})
	assert.NilError(t, err, "create vm")

	// check try catch & global context
	_, err = vm.RunScriptWithTimeout(`
try {
	get();
} catch(err) {}
try {
	list();
} catch(err) {}
try {
	create();
} catch(err) {}
try {
	update();
} catch(err) {}
try {
	remove();
} catch(err) {}

var testGlobal = "test";
`, "test", time.Second)
	assert.NilError(t, err, "test throw")
	testVal, err := vm.Context().Global().Get("testGlobal")
	assert.NilError(t, err)
	assert.Equal(t, testVal.String(), "test")

	// check recreate context
	err = vm.RecreateContext()
	assert.NilError(t, err, "recreate context")

	// check exit() & print()
	_, err = vm.RunScriptWithTimeout(`
var __policy = "test";
print("test message");
exit();
print("test second message");
`, "test", time.Second)
	assert.NilError(t, err, "test")
	assert.Equal(t, len(printed), 1)
	assert.Equal(t, printed[0], "[test] test message")

	// check timeout
	_, err = vm.RunScriptWithTimeout(`while(true) {}`, "test", time.Millisecond*50)
	assert.Error(t, err, "script test timed out")

	// check throw
	_, err = vm.RunScriptWithTimeout(`
throw "test"
`, "test", time.Second)
	assert.Error(t, err, "test")

	// btoa atob
	_, err = vm.RunScriptWithTimeout(`
const test = "test";
if (atob(btoa(test)) !== test) {
	throw "Error!"
}
`, "test", time.Second)
	assert.NilError(t, err, "btoa")
}
