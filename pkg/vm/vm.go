package vm

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	cache2 "github.com/loft-sh/jspolicy/pkg/cache"
	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"rogchap.com/v8go"
	clientpkg "sigs.k8s.io/controller-runtime/pkg/client"
)

type LogFunc func(str string)

type VM interface {
	Context() *v8go.Context
	RunScriptWithTimeout(script string, origin string, timeout time.Duration) (*v8go.Value, error)
	RecreateContext() error
}

type vm struct {
	isolate *v8go.Isolate
	global  *v8go.ObjectTemplate
	context *v8go.Context
}

func NewVM(cachedClient cache2.Cache, uncachedClient clientpkg.Client, log LogFunc) (VM, error) {
	iso := v8go.NewIsolate()
	global := v8go.NewObjectTemplate(iso)

	printFn := printFn(iso, log)
	err := global.Set("print", printFn, v8go.ReadOnly)
	if err != nil {
		return nil, err
	}

	sleepFn := sleepFn(iso)
	err = global.Set("sleep", sleepFn, v8go.ReadOnly)
	if err != nil {
		return nil, err
	}

	getFn := getFn(iso, cachedClient, uncachedClient)
	err = global.Set("__get", getFn, v8go.ReadOnly)
	if err != nil {
		return nil, err
	}

	atobFn := atobFn(iso)
	err = global.Set("atob", atobFn, v8go.ReadOnly)
	if err != nil {
		return nil, err
	}

	btoaFn := btoaFn(iso)
	err = global.Set("btoa", btoaFn, v8go.ReadOnly)
	if err != nil {
		return nil, err
	}

	exitFn := exitFn(iso)
	err = global.Set("__exit", exitFn, v8go.ReadOnly)
	if err != nil {
		return nil, err
	}

	listFn := listFn(iso, cachedClient, uncachedClient)
	err = global.Set("__list", listFn, v8go.ReadOnly)
	if err != nil {
		return nil, err
	}

	fetchFn := fetchFn(iso)
	err = global.Set("__fetchSync", fetchFn, v8go.ReadOnly)
	if err != nil {
		return nil, err
	}

	envFn := envFn(iso)
	err = global.Set("env", envFn, v8go.ReadOnly)
	if err != nil {
		return nil, err
	}

	readFileFn := readFileFn(iso)
	err = global.Set("readFileSync", readFileFn, v8go.ReadOnly)
	if err != nil {
		return nil, err
	}

	createFn := createFn(iso, uncachedClient)
	err = global.Set("__create", createFn, v8go.ReadOnly)
	if err != nil {
		return nil, err
	}

	updateFn := updateFn(iso, uncachedClient)
	err = global.Set("__update", updateFn, v8go.ReadOnly)
	if err != nil {
		return nil, err
	}

	removeFn := removeFn(iso, uncachedClient)
	err = global.Set("__remove", removeFn, v8go.ReadOnly)
	if err != nil {
		return nil, err
	}

	ctx, err := NewContext(iso, global)
	if err != nil {
		return nil, err
	}

	return &vm{
		isolate: iso,
		global:  global,
		context: ctx,
	}, nil
}

func NewContext(isolate *v8go.Isolate, global *v8go.ObjectTemplate) (*v8go.Context, error) {
	ctx := v8go.NewContext(isolate, global)
	_, err := (&vm{
		isolate: isolate,
		global:  global,
		context: ctx,
	}).RunScriptWithTimeout(`
var exports = {};
var __response = {};
var __throw = (result) => {
	if (result?.__throw) {
		throw {reason: result.reason, message: result.message, toString: () => result.reason + ": " + result.message}
	}
	return result;
};
var exit = () => {
	__exit();
	while(true) {}
};
var warn = (message) => {
	if (!__response.warnings) {
		__response.warnings = [];
	}
	__response.warnings.push(message);
};
var deny = (message, reason, code) => {
        __response.deny = true;
        __response.message = message;
        __response.reason = reason;
        __response.code = code;
        exit();
};
var list = (kind, groupVersion, options) => {
	return __throw(__list(kind, groupVersion, options)).list;
};
var create = (object) => {
	return __throw(__create(object));
};
var update = (object) => {
	return __throw(__update(object));
};
var remove = (object) => {
	return __throw(__remove(object));
};
var get = (kind, groupVersion, name, options) => {
	if (options?.cache === 'smart') {
		return __throw(__get(kind, groupVersion, name)).object || __throw(__get(kind, groupVersion, name, {cache: false})).object;
	}
	
	return __throw(__get(kind, groupVersion, name, options)).object;
};
var allow = () => exit();
var mutate = (obj) => {
	__response.patched = obj;
	exit();
};
var fetchSync = (url, options) => {
	const res = __fetchSync(url, options);
	if (res.__throw) {
		throw(res.__throw);
	}

	return {
        text:       () => res.__body,
        json:       () => JSON.parse(res.__body),
		headers:    res.headers,
        status:     res.status,
        statusText: res.statusText,
        ok:         res.status >= 200 && res.status < 300
	}
};
var requeue = (message) => {
	__response.reschedule = true;
	__response.message = message;
	exit();
};

`+os.Getenv("GLOBAL_CONTEXT")+`
`, "prepare-context.js", time.Second*10)
	if err != nil {
		ctx.Close()
		return nil, errors.Wrap(err, "prepare context")
	}

	return ctx, nil
}

func (v *vm) Context() *v8go.Context {
	return v.context
}

func (v *vm) RecreateContext() error {
	newCtx, err := NewContext(v.isolate, v.global)
	if err != nil {
		return err
	}

	v.context.Close()
	v.context = newCtx
	return nil
}

func (v *vm) RunScriptWithTimeout(script string, origin string, timeout time.Duration) (*v8go.Value, error) {
	var (
		val *v8go.Value
		err error
	)

	errorChan := make(chan error, 1)
	go func() {
		val, err = v.runScript(script, origin)
		errorChan <- err
	}()
	select {
	case err := <-errorChan:
		return val, err
	case <-time.After(timeout):
		v.isolate.TerminateExecution()
		<-errorChan
		return nil, fmt.Errorf("script %s timed out", origin)
	}
}

func (v *vm) runScript(script string, origin string) (val *v8go.Value, err error) {
	val, err = v.context.RunScript(script, origin)
	if err != nil {
		jsErr, ok := err.(*v8go.JSError)
		if ok && jsErr != nil && jsErr.Message == "ExecutionTerminated: script execution has been terminated" {
			return nil, nil
		} else if ok && jsErr != nil {
			if jsErr.StackTrace != "" {
				return nil, fmt.Errorf("Uncaught %s", jsErr.StackTrace)
			}

			return nil, jsErr
		}
	}

	return val, err
}

func sleepFn(iso *v8go.Isolate) *v8go.FunctionTemplate {
	return v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
		if len(info.Args()) != 1 {
			return nil
		}

		rawNumber := info.Args()[0]
		if rawNumber.IsNumber() == false {
			return nil
		}

		number := rawNumber.Number()
		if number > 1000 {
			number = 1000
		}

		time.Sleep(time.Millisecond * time.Duration(number))
		return nil
	})
}

func btoaFn(iso *v8go.Isolate) *v8go.FunctionTemplate {
	return v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
		if len(info.Args()) != 1 {
			return nil
		}

		rawString := info.Args()[0]
		if rawString.IsString() == false {
			return nil
		}

		encodedString, err := v8go.NewValue(iso, base64.StdEncoding.EncodeToString([]byte(rawString.String())))
		if err != nil {
			return nil
		}

		return encodedString
	})
}

func atobFn(iso *v8go.Isolate) *v8go.FunctionTemplate {
	return v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
		if len(info.Args()) != 1 {
			return nil
		}

		base64Encoded := info.Args()[0]
		if base64Encoded.IsString() == false {
			return nil
		}

		decoded, err := base64.StdEncoding.DecodeString(base64Encoded.String())
		if err != nil {
			return nil
		}

		decodedString, err := v8go.NewValue(iso, string(decoded))
		if err != nil {
			return nil
		}

		return decodedString
	})
}

func listFn(iso *v8go.Isolate, cachedClient clientpkg.Reader, uncachedClient clientpkg.Client) *v8go.FunctionTemplate {
	return v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
		if len(info.Args()) != 3 && len(info.Args()) != 2 {
			return jsonResponse(info.Context(), &ListResponse{
				Throw:   true,
				Reason:  "WrongArguments",
				Message: "three arguments expected. Example list(\"Pod\", \"v1\")",
			})
		}

		args := info.Args()
		kind, groupVersion := args[0], args[1]
		if !kind.IsString() || !groupVersion.IsString() {
			return jsonResponse(info.Context(), &ListResponse{
				Throw:   true,
				Reason:  "WrongArguments",
				Message: "kind and group version need to be string values",
			})
		}

		kindList := kind.String()
		if strings.HasSuffix(kindList, "List") == false {
			kindList += "List"
		}

		var listOptions *ListOptions
		if len(args) == 3 && args[2].IsUndefined() == false {
			jsonString, err := v8go.JSONStringify(info.Context(), args[2])
			if err != nil {
				return jsonResponse(info.Context(), &ListResponse{
					Throw:   true,
					Reason:  "StringifyOptions",
					Message: err.Error(),
				})
			}

			listOptions = &ListOptions{}
			err = json.Unmarshal([]byte(jsonString), listOptions)
			if err != nil {
				return jsonResponse(info.Context(), &ListResponse{
					Throw:   true,
					Reason:  "UnmarshalOptions",
					Message: err.Error(),
				})
			}
		}

		unstructuredList := &unstructured.UnstructuredList{}
		unstructuredList.SetKind(kindList)
		unstructuredList.SetAPIVersion(groupVersion.String())

		clientListOptions := []clientpkg.ListOption{}
		if listOptions != nil && listOptions.Namespace != "" {
			clientListOptions = append(clientListOptions, clientpkg.InNamespace(listOptions.Namespace))
		}
		if listOptions != nil && listOptions.LabelSelector != "" {
			labelSelector, err := metav1.ParseToLabelSelector(listOptions.LabelSelector)
			if err != nil {
				return jsonResponse(info.Context(), &ListResponse{
					Throw:   true,
					Reason:  "ParseLabelSelector",
					Message: err.Error(),
				})
			} else if labelSelector.MatchLabels != nil {
				clientListOptions = append(clientListOptions, clientpkg.MatchingLabels(labelSelector.MatchLabels))
			}
		}

		// which client should we use?
		client := cachedClient
		if listOptions != nil && listOptions.Cache != nil && *listOptions.Cache == false {
			client = uncachedClient
		}

		// list resources
		err := client.List(context.Background(), unstructuredList, clientListOptions...)
		if err != nil {
			return jsonResponse(info.Context(), &ListResponse{
				Throw:   true,
				Reason:  "ErrorListing",
				Message: err.Error(),
			})
		}

		// extract list
		list, err := meta.ExtractList(unstructuredList)
		if err != nil {
			return jsonResponse(info.Context(), &ListResponse{
				Throw:   true,
				Reason:  "ExtractList",
				Message: err.Error(),
			})
		}

		return jsonResponse(info.Context(), &ListResponse{
			List: list,
		})
	})
}

type GetOptions struct {
	// If false will not use the cache for the request
	Cache *bool `json:"cache,omitempty"`
}

type GetResponse struct {
	Throw   bool   `json:"__throw,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`

	Object *unstructured.Unstructured `json:"object,omitempty"`
}

type ListOptions struct {
	// A selector to restrict the list of returned objects by their labels.
	// Defaults to everything.
	// +optional
	LabelSelector string `json:"labelSelector,omitempty"`

	// Namespace to limit the search to
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// If false will not use the cache for the request
	Cache *bool `json:"cache,omitempty"`
}

type ListResponse struct {
	Throw   bool   `json:"__throw,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`

	List []runtime.Object `json:"list,omitempty"`
}

func getFn(iso *v8go.Isolate, cachedClient clientpkg.Reader, uncachedClient clientpkg.Client) *v8go.FunctionTemplate {
	return v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
		if len(info.Args()) != 3 && len(info.Args()) != 4 {
			return jsonResponse(info.Context(), &GetResponse{
				Throw:   true,
				Reason:  "WrongArguments",
				Message: "three arguments expected. Example get(\"Pod\", \"v1\", \"test\")",
			})
		}

		args := info.Args()
		kind, groupVersion, name := args[0], args[1], args[2]
		if !kind.IsString() || !groupVersion.IsString() || !name.IsString() {
			return jsonResponse(info.Context(), &GetResponse{
				Throw:   true,
				Reason:  "WrongArguments",
				Message: "kind, group version and name need to be string values",
			})
		}

		var getOptions *GetOptions
		if len(args) == 4 && args[3].IsUndefined() == false {
			jsonString, err := v8go.JSONStringify(info.Context(), args[3])
			if err != nil {
				return jsonResponse(info.Context(), &GetResponse{
					Throw:   true,
					Reason:  "StringifyOptions",
					Message: err.Error(),
				})
			}

			getOptions = &GetOptions{}
			err = json.Unmarshal([]byte(jsonString), getOptions)
			if err != nil {
				return jsonResponse(info.Context(), &GetResponse{
					Throw:   true,
					Reason:  "UnmarshalOptions",
					Message: err.Error(),
				})
			}
		}

		// create return object
		obj := &unstructured.Unstructured{}
		obj.SetKind(kind.String())
		obj.SetAPIVersion(groupVersion.String())

		// check if we have a namespace
		namespacedName := types.NamespacedName{Name: name.String()}
		splittedName := strings.Split(name.String(), "/")
		if len(splittedName) == 2 {
			namespacedName.Namespace = splittedName[0]
			namespacedName.Name = splittedName[1]
		}

		// which client should we use?
		client := cachedClient
		if getOptions != nil && getOptions.Cache != nil && *getOptions.Cache == false {
			client = uncachedClient
		}

		// get resource
		err := client.Get(context.Background(), namespacedName, obj)
		if err != nil {
			if kerrors.IsNotFound(err) {
				return jsonResponse(info.Context(), &GetResponse{
					Object: nil,
				})
			}

			return jsonResponse(info.Context(), &GetResponse{
				Throw:   true,
				Reason:  "RetrieveObject",
				Message: err.Error(),
			})
		}

		return jsonResponse(info.Context(), &GetResponse{
			Object: obj,
		})
	})
}

func exitFn(iso *v8go.Isolate) *v8go.FunctionTemplate {
	return v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
		iso.TerminateExecution()
		return nil
	})
}

func printFn(iso *v8go.Isolate, log LogFunc) *v8go.FunctionTemplate {
	return v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
		origin, err := info.Context().Global().Get("__policy")
		if err != nil {
			klog.Warning(err)
			return nil
		}

		out := []string{}
		for _, a := range info.Args() {
			if a.IsString() {
				out = append(out, a.String())
			} else {
				s, err := v8go.JSONStringify(info.Context(), a)
				if err == nil {
					out = append(out, s)
				}
			}
		}
		log("[" + origin.String() + "] " + strings.Join(out, " "))
		return nil
	})
}

func fetchFn(iso *v8go.Isolate) *v8go.FunctionTemplate {
	return v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
		args := info.Args()
		if len(args) == 0 {
			return returnFetchError(info.Context(), fmt.Errorf("no arguments provided"))
		}

		url := args[0]
		if !url.IsString() {
			return returnFetchError(info.Context(), fmt.Errorf("url is not a string"))
		}

		options := &FetchOptions{}
		if len(args) > 1 && args[1].IsUndefined() == false {
			out, err := v8go.JSONStringify(info.Context(), args[1])
			if err != nil {
				return returnFetchError(info.Context(), fmt.Errorf("error stringify options: %v", err))
			}

			err = json.Unmarshal([]byte(out), options)
			if err != nil {
				return returnFetchError(info.Context(), fmt.Errorf("error unmarshalling options: %v", err))
			}
		}

		method := "GET"
		if options.Method != "" {
			method = strings.ToUpper(options.Method)
		}

		var body io.Reader
		if options.Body != "" {
			body = strings.NewReader(options.Body)
		}

		req, err := http.NewRequest(method, url.String(), body)
		if err != nil {
			return returnFetchError(info.Context(), fmt.Errorf("create request: %v", err))
		}

		for k, v := range options.Headers {
			req.Header.Add(k, v)
		}

		client := http.DefaultClient
		if options.Insecure {
			customTransport := http.DefaultTransport.(*http.Transport).Clone()
			customTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			client = &http.Client{Transport: customTransport}
		}

		res, err := client.Do(req)
		if err != nil {
			return returnFetchError(info.Context(), err)
		}

		responseObject := &FetchResponse{
			Status:     res.StatusCode,
			StatusText: res.Status,
		}

		// read body
		responseBodyBytes, err := ioutil.ReadAll(res.Body)
		if err != nil {
			return returnFetchError(info.Context(), errors.Wrap(err, "read body"))
		}

		// set response headers
		responseObject.Headers = map[string]string{}
		for k := range res.Header {
			responseObject.Headers[k] = res.Header.Get(k)
		}

		responseObject.Body = string(responseBodyBytes)
		out, err := json.Marshal(responseObject)
		if err != nil {
			klog.Infof("fetch: error marshaling fetch response: " + err.Error())
			return nil
		}

		val, err := v8go.JSONParse(info.Context(), string(out))
		if err != nil {
			klog.Infof("fetch: error parsing fetch response: " + err.Error())
			return nil
		}
		return val
	})
}

func returnFetchError(ctx *v8go.Context, fetchErr error) *v8go.Value {
	out, err := json.Marshal(&FetchResponse{
		Throw: "fetchSync: " + fetchErr.Error(),
	})
	if err != nil {
		klog.Infof("fetch: error marshaling fetch response: " + err.Error())
		return nil
	}

	val, err := v8go.JSONParse(ctx, string(out))
	if err != nil {
		klog.Infof("fetch: error parsing fetch response: " + err.Error())
		return nil
	}

	return val
}

type FetchOptions struct {
	Method   string            `json:"method,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Insecure bool              `json:"insecure,omitempty"`
	Body     string            `json:"body,omitempty"`
}

type FetchResponse struct {
	Throw string `json:"__throw,omitempty"`
	Body  string `json:"__body,omitempty"`

	Headers    map[string]string `json:"headers,omitempty"`
	Status     int               `json:"status,omitempty"`
	StatusText string            `json:"statusText,omitempty"`
}

func envFn(iso *v8go.Isolate) *v8go.FunctionTemplate {
	return v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
		if len(info.Args()) != 1 {
			return nil
		}

		envName := info.Args()[0]
		if !envName.IsString() {
			return nil
		}

		retEnv, err := v8go.NewValue(iso, os.Getenv(envName.String()))
		if err != nil {
			return nil
		}

		return retEnv
	})
}

func readFileFn(iso *v8go.Isolate) *v8go.FunctionTemplate {
	return v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
		if len(info.Args()) != 1 {
			return nil
		}

		fileName := info.Args()[0]
		if !fileName.IsString() {
			return nil
		}

		out, err := ioutil.ReadFile(fileName.String())
		if err != nil {
			return nil
		}

		retEnv, err := v8go.NewValue(iso, string(out))
		if err != nil {
			return nil
		}

		return retEnv
	})
}

type ResourceResponse struct {
	Throw bool `json:"__throw,omitempty"`

	Object map[string]interface{} `json:"object,omitempty"`

	Ok      bool   `json:"ok"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

func createFn(iso *v8go.Isolate, uncachedClient clientpkg.Client) *v8go.FunctionTemplate {
	return v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
		if len(info.Args()) != 1 {
			return jsonResponse(info.Context(), &ResourceResponse{
				Throw:   true,
				Reason:  "WrongArguments",
				Message: "single argument expected",
			})
		}

		object := info.Args()[0]
		str, err := v8go.JSONStringify(info.Context(), object)
		if err != nil {
			return jsonResponse(info.Context(), &ResourceResponse{
				Throw:   true,
				Reason:  "StringifyObject",
				Message: err.Error(),
			})
		}

		objParsed := &unstructured.Unstructured{
			Object: map[string]interface{}{},
		}
		err = json.Unmarshal([]byte(str), &objParsed.Object)
		if err != nil {
			return jsonResponse(info.Context(), &ResourceResponse{
				Throw:   true,
				Reason:  "UnmarshalObject",
				Message: err.Error(),
			})
		}

		returnValue := ResourceResponse{}
		err = uncachedClient.Create(context.Background(), objParsed)
		if err != nil {
			reason := kerrors.ReasonForError(err)
			returnValue.Reason = string(reason)
			returnValue.Message = err.Error()
		} else {
			returnValue.Ok = true
			returnValue.Object = objParsed.Object
		}

		return jsonResponse(info.Context(), returnValue)
	})
}

func updateFn(iso *v8go.Isolate, uncachedClient clientpkg.Client) *v8go.FunctionTemplate {
	return v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
		if len(info.Args()) != 1 {
			return jsonResponse(info.Context(), &ResourceResponse{
				Throw:   true,
				Reason:  "WrongArguments",
				Message: "single argument expected",
			})
		}

		object := info.Args()[0]
		str, err := v8go.JSONStringify(info.Context(), object)
		if err != nil {
			return jsonResponse(info.Context(), &ResourceResponse{
				Throw:   true,
				Reason:  "StringifyObject",
				Message: err.Error(),
			})
		}

		objParsed := &unstructured.Unstructured{
			Object: map[string]interface{}{},
		}
		err = json.Unmarshal([]byte(str), &objParsed.Object)
		if err != nil {
			return jsonResponse(info.Context(), &ResourceResponse{
				Throw:   true,
				Reason:  "UnmarshalObject",
				Message: err.Error(),
			})
		}

		returnValue := ResourceResponse{}
		err = uncachedClient.Update(context.Background(), objParsed)
		if err != nil {
			reason := kerrors.ReasonForError(err)
			returnValue.Reason = string(reason)
			returnValue.Message = err.Error()
		} else {
			returnValue.Ok = true
			returnValue.Object = objParsed.Object
		}

		return jsonResponse(info.Context(), returnValue)
	})
}

func jsonResponse(ctx *v8go.Context, value interface{}) *v8go.Value {
	strVal, err := json.Marshal(value)
	if err != nil {
		klog.Infof("marshal response: " + err.Error())
		return nil
	}

	response, err := v8go.JSONParse(ctx, string(strVal))
	if err != nil {
		klog.Infof("json parse: " + err.Error())
		return nil
	}

	return response
}

func removeFn(iso *v8go.Isolate, uncachedClient clientpkg.Client) *v8go.FunctionTemplate {
	return v8go.NewFunctionTemplate(iso, func(info *v8go.FunctionCallbackInfo) *v8go.Value {
		if len(info.Args()) != 1 {
			return jsonResponse(info.Context(), &ResourceResponse{
				Ok:      false,
				Reason:  "WrongArguments",
				Message: "single argument expected",
			})
		}

		object := info.Args()[0]
		str, err := v8go.JSONStringify(info.Context(), object)
		if err != nil {
			return jsonResponse(info.Context(), &ResourceResponse{
				Ok:      false,
				Reason:  "StringifyObject",
				Message: err.Error(),
			})
		}

		objParsed := &unstructured.Unstructured{
			Object: map[string]interface{}{},
		}
		err = json.Unmarshal([]byte(str), &objParsed.Object)
		if err != nil {
			return jsonResponse(info.Context(), &ResourceResponse{
				Ok:      false,
				Reason:  "UnmarshalObject",
				Message: err.Error(),
			})
		}

		returnValue := ResourceResponse{}
		err = uncachedClient.Delete(context.Background(), objParsed)
		if err != nil {
			reason := kerrors.ReasonForError(err)
			returnValue.Reason = string(reason)
			returnValue.Message = err.Error()
		} else {
			returnValue.Ok = true
			returnValue.Object = objParsed.Object
		}

		return jsonResponse(info.Context(), returnValue)
	})
}
