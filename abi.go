package main

/*
#include <stdint.h>
#include <stdlib.h>
typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct { uint32_t abi_version; void* host_ctx; cliproxy_host_call_fn call; cliproxy_host_free_fn free_buffer; } cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct { uint32_t abi_version; cliproxy_plugin_call_fn call; cliproxy_plugin_free_fn free_buffer; cliproxy_plugin_shutdown_fn shutdown; } cliproxy_plugin_api;
extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
static int dock_host_call(cliproxy_host_api* a, const char* m, const uint8_t* r, size_t n, cliproxy_buffer* out) { return a->call(a->host_ctx,m,r,n,out); }
static void dock_host_free(cliproxy_host_api* a, void* p, size_t n) { a->free_buffer(p,n); }
*/
import "C"

import (
	"errors"
	"sync"
	"unsafe"

	"github.com/cpa-model-dock/cpa-model-dock/internal/hostabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// The host owns this table until shutdown. Holding the read lock across a call
// guarantees its allocator stays available until the response has been freed.
var native struct {
	sync.RWMutex
	host *C.cliproxy_host_api
}
var instance = newRuntime(hostabi.Client{Transport: callHost})

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil || uint32(host.abi_version) != pluginabi.ABIVersion || host.call == nil || host.free_buffer == nil {
		return 1
	}
	native.Lock()
	native.host = host
	native.Unlock()
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, n C.size_t, out *C.cliproxy_buffer) C.int {
	if out == nil {
		return 1
	}
	out.ptr = nil
	out.len = 0
	var result []byte
	if method == nil || n > C.size_t(1<<31-1) || (n > 0 && request == nil) {
		result, _ = pluginabi.NewErrorEnvelope("invalid_request", "invalid ABI request")
	} else {
		var b []byte
		if n > 0 {
			b = C.GoBytes(unsafe.Pointer(request), C.int(n))
		}
		result = instance.Dispatch(C.GoString(method), b)
	}
	out.ptr = C.CBytes(result)
	if out.ptr == nil {
		return 1
	}
	out.len = C.size_t(len(result))
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, n C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { instance.Close(); native.Lock(); native.host = nil; native.Unlock() }

func callHost(method string, payload []byte) ([]byte, error) {
	native.RLock()
	defer native.RUnlock()
	if native.host == nil {
		return nil, errors.New("host callback unavailable")
	}
	m := C.CString(method)
	defer C.free(unsafe.Pointer(m))
	var p unsafe.Pointer
	if len(payload) > 0 {
		p = C.CBytes(payload)
		defer C.free(p)
	}
	var out C.cliproxy_buffer
	rc := C.dock_host_call(native.host, m, (*C.uint8_t)(p), C.size_t(len(payload)), &out)
	if out.ptr != nil {
		defer C.dock_host_free(native.host, out.ptr, out.len)
	}
	if rc != 0 || out.len > C.size_t(1<<31-1) {
		return nil, errors.New("host callback failed")
	}
	if out.ptr == nil {
		return nil, errors.New("empty host callback")
	}
	return C.GoBytes(out.ptr, C.int(out.len)), nil
}
