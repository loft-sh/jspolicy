package bundle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/loft-sh/jspolicy/pkg/util/compress"
	"github.com/pkg/errors"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	WebpackConfig = `const path = require('path');

module.exports = {
    mode: 'production',
    entry: './index.js',
    output: {
        path: path.resolve(__dirname, 'dist'),
        filename: 'bundle.js',
        libraryTarget: 'commonjs',
    },
};`
)

func init() {
	webpackConfig := os.Getenv("WEBPACK_CONFIG")
	if webpackConfig != "" {
		WebpackConfig = webpackConfig
	}
}

type JavascriptBundler interface {
	Bundle(javascript string, dependencies map[string]string, timeout time.Duration) ([]byte, error)
}

func NewJavascriptBundler() JavascriptBundler {
	return &javascriptBundler{
		commandWithTimeout: commandWithTimeout,
	}
}

type javascriptBundler struct {
	commandWithTimeout commandWithTimeoutFn
}

func (j *javascriptBundler) Bundle(javascript string, dependencies map[string]string, timeout time.Duration) ([]byte, error) {
	tempDir, err := ioutil.TempDir("", "")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)

	// create a package json in the temp dir
	packageJson, err := json.Marshal(map[string]interface{}{
		"name":         "my_package",
		"description":  "this is a temporary package",
		"version":      "1.0.0",
		"main":         "./index.js",
		"dependencies": dependencies,
	})
	if err != nil {
		return nil, errors.Wrap(err, "marshal package json")
	}

	// write the index.js
	err = ioutil.WriteFile(filepath.Join(tempDir, "index.js"), []byte(javascript), 0666)
	if err != nil {
		return nil, errors.Wrap(err, "write index js")
	}

	// write the package json
	err = ioutil.WriteFile(filepath.Join(tempDir, "package.json"), packageJson, 0666)
	if err != nil {
		return nil, errors.Wrap(err, "write package json")
	}

	// write the webpack config
	err = ioutil.WriteFile(filepath.Join(tempDir, "webpack.config.js"), []byte(WebpackConfig), 0666)
	if err != nil {
		return nil, errors.Wrap(err, "write webpack config")
	}

	// install node modules if necessary
	if len(dependencies) > 0 {
		err = j.commandWithTimeout(tempDir, timeout, "npm", "install", "--disable-scripts")
		if err != nil {
			return nil, fmt.Errorf("error installing dependencies: %v", err)
		}
	}

	// bundle via webpack
	err = j.commandWithTimeout(tempDir, timeout, "webpack-cli", "--config", "webpack.config.js")
	if err != nil {
		return nil, fmt.Errorf("error bundling javascript: %v", err)
	}

	// get the output
	out, err := ioutil.ReadFile(filepath.Join(tempDir, "dist", "bundle.js"))
	if err != nil {
		return nil, errors.Wrap(err, "read bundle.js")
	}

	return compress.Compress(string(out))
}

// used for testing
type commandWithTimeoutFn func(folder string, timeout time.Duration, name string, args ...string) error

func commandWithTimeout(folder string, timeout time.Duration, name string, args ...string) error {
	buf := &Buffer{}

	cmd := exec.Command(name, args...)
	cmd.Dir = folder
	cmd.Stdout = buf
	cmd.Stderr = buf

	// Start command
	err := cmd.Start()
	if err != nil {
		return err
	}

	// Use a channel to signal completion so we can use a select statement
	done := make(chan error)
	go func() { done <- cmd.Wait() }()

	// The select statement allows us to execute based on which channel
	// we get a message from first.
	select {
	case <-time.After(timeout):
		// Timeout happened first, kill the process and print a message.
		_ = cmd.Process.Kill()
		return fmt.Errorf("the command %s %s timed out after %s", name, strings.Join(args, " "), timeout)
	case err := <-done:
		// Command completed before timeout. Print output and error if it exists.
		if err != nil {
			return fmt.Errorf("%s %v", buf.String(), err)
		}
	}

	return nil
}

type Buffer struct {
	b bytes.Buffer
	m sync.Mutex
}

func (b *Buffer) Read(p []byte) (n int, err error) {
	b.m.Lock()
	defer b.m.Unlock()
	return b.b.Read(p)
}
func (b *Buffer) Write(p []byte) (n int, err error) {
	b.m.Lock()
	defer b.m.Unlock()
	return b.b.Write(p)
}
func (b *Buffer) String() string {
	b.m.Lock()
	defer b.m.Unlock()
	return b.b.String()
}
