package bundle

import (
	"github.com/loft-sh/jspolicy/pkg/util/compress"
	"gotest.tools/assert"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var bundle = "test"

func fakeExecuteCommand(folder string, timeout time.Duration, name string, args ...string) error {
	err := os.MkdirAll(filepath.Join(folder, "dist"), 0777)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(filepath.Join(folder, "dist", "bundle.js"), []byte(bundle), 0755)
	if err != nil {
		return err
	}

	return nil
}

func TestSimple(t *testing.T) {
	called := 0
	bundler := &javascriptBundler{
		commandWithTimeout: func(folder string, timeout time.Duration, name string, args ...string) error {
			assert.Equal(t, name, "webpack-cli", "command with output")
			called += 1
			return fakeExecuteCommand(folder, timeout, name, args...)
		},
	}

	compressedBundle, err := compress.Compress(bundle)
	assert.NilError(t, err)

	// bundle without dependencies
	out, err := bundler.Bundle("irrelevant", nil, time.Second)
	assert.NilError(t, err)
	assert.Equal(t, string(out), string(compressedBundle))
	assert.Equal(t, called, 1)

	// bundle with dependencies
	called = 0
	bundler.commandWithTimeout = func(folder string, timeout time.Duration, name string, args ...string) error {
		if name != "npm" && name != "webpack-cli" {
			t.Fatalf("unexpected command for bundler: " + name)
		}
		called += 1
		return fakeExecuteCommand(folder, timeout, name, args...)
	}
	out, err = bundler.Bundle("irrelevant", map[string]string{"test": "test"}, time.Second)
	assert.NilError(t, err)
	assert.Equal(t, string(out), string(compressedBundle))
	assert.Equal(t, called, 2)
}
