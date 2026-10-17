//go:build cgo && (linux || darwin || freebsd)

package pluginhost

/*
#cgo linux LDFLAGS: -ldl
#cgo freebsd LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

typedef int (*cliproxy_plugin_init_fn)(const cliproxy_host_api*, cliproxy_plugin_api*);

extern int cliproxyHostCall(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyHostFree(void*, size_t);

static void* cliproxy_dlopen(const char* path) {
	return dlopen(path, RTLD_NOW | RTLD_LOCAL);
}

static void* cliproxy_dlsym(void* handle, const char* name) {
	return dlsym(handle, name);
}

static const char* cliproxy_dlerror(void) {
	return dlerror();
}

static int cliproxy_call_init(void* fn, const cliproxy_host_api* host, cliproxy_plugin_api* plugin) {
	return ((cliproxy_plugin_init_fn)fn)(host, plugin);
}

static int cliproxy_call_plugin(cliproxy_plugin_call_fn fn, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	return fn(method, request, request_len, response);
}

static void cliproxy_free_plugin_buffer(cliproxy_plugin_free_fn fn, void* ptr, size_t len) {
	fn(ptr, len);
}

static void cliproxy_shutdown_plugin(cliproxy_plugin_shutdown_fn fn) {
	fn();
}

static void cliproxy_set_host_api(cliproxy_host_api* api, uint32_t abi_version, void* host_ctx) {
	api->abi_version = abi_version;
	api->host_ctx = host_ctx;
	api->call = cliproxyHostCall;
	api->free_buffer = cliproxyHostFree;
}

*/
import "C"

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"
)

var (
	hostCallbackID      atomic.Uintptr
	hostCallbackEntries sync.Map
)

type dynamicLibraryLoader struct{}

type dynamicLibraryClient struct {
	handle   unsafe.Pointer
	tempPath string
	hostAPI  *C.cliproxy_host_api
	hostCtx  unsafe.Pointer
	api      C.cliproxy_plugin_api
	host     *Host
	pluginID string
	instance *hostCallbackInstance
}

func defaultPluginLoader() pluginLoader {
	return dynamicLibraryLoader{}
}

func (dynamicLibraryLoader) Open(file pluginFile, host *Host) (pluginClient, error) {
	loadPath, errShadow := shadowCopyPlugin(file)
	if errShadow != nil {
		return nil, errShadow
	}
	cPath := C.CString(loadPath)
	defer C.free(unsafe.Pointer(cPath))

	handle := C.cliproxy_dlopen(cPath)
	if handle == nil {
		removeShadowPlugin(loadPath)
		return nil, fmt.Errorf("dlopen %s: %s", file.Path, dlerrorString())
	}
	// 从这里开始，该映像绝不能卸载：dlopen 可能已经
	// 启动了插件运行时，而 Go c-shared 运行时无法
	// 从宿主进程中安全移除。下面的失败路径只是
	// 在逻辑上分离客户端，并有意泄漏已映射的映像
	// 以及运行时可能引用的任何宿主回调状态。

	cSymbol := C.CString("cliproxy_plugin_init")
	initSymbol := C.cliproxy_dlsym(handle, cSymbol)
	C.free(unsafe.Pointer(cSymbol))
	if initSymbol == nil {
		return nil, fmt.Errorf("missing cliproxy_plugin_init: %s", dlerrorString())
	}

	hostAPI := (*C.cliproxy_host_api)(C.malloc(C.size_t(unsafe.Sizeof(C.cliproxy_host_api{}))))
	if hostAPI == nil {
		return nil, fmt.Errorf("allocate host api")
	}
	hostCtx := C.malloc(C.size_t(unsafe.Sizeof(C.uintptr_t(0))))
	if hostCtx == nil {
		return nil, fmt.Errorf("allocate host context")
	}
	id := hostCallbackID.Add(1)
	instance := &hostCallbackInstance{}
	host.registerHostCallbackInstance(file.ID, instance)
	*(*C.uintptr_t)(hostCtx) = C.uintptr_t(id)
	hostCallbackEntries.Store(id, dynamicHostCallbackEntry{host: host, pluginID: file.ID, instance: instance})
	C.cliproxy_set_host_api(hostAPI, C.uint32_t(pluginHostABIVersion), hostCtx)

	client := &dynamicLibraryClient{
		handle:   handle,
		tempPath: loadPath,
		hostAPI:  hostAPI,
		hostCtx:  hostCtx,
		host:     host,
		pluginID: file.ID,
		instance: instance,
	}
	rc := C.cliproxy_call_init(initSymbol, hostAPI, &client.api)
	if rc != 0 {
		client.closeAfterOpenFailure()
		return nil, fmt.Errorf("cliproxy_plugin_init returned %d", int(rc))
	}
	if uint32(client.api.abi_version) != pluginHostABIVersion {
		client.closeAfterOpenFailure()
		return nil, fmt.Errorf("plugin ABI version %d is not supported", uint32(client.api.abi_version))
	}
	if client.api.call == nil || client.api.free_buffer == nil {
		client.closeAfterOpenFailure()
		return nil, fmt.Errorf("plugin function table is incomplete")
	}
	return client, nil
}

func (c *dynamicLibraryClient) callbackInstance() *hostCallbackInstance {
	if c == nil {
		return nil
	}
	return c.instance
}

func (c *dynamicLibraryClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	if c == nil || c.api.call == nil {
		return nil, fmt.Errorf("plugin client is closed")
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}

	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var cRequest unsafe.Pointer
	if len(request) > 0 {
		cRequest = C.CBytes(request)
		defer C.free(cRequest)
	}
	var response C.cliproxy_buffer
	rc := C.cliproxy_call_plugin(c.api.call, cMethod, (*C.uint8_t)(cRequest), C.size_t(len(request)), &response)
	var out []byte
	if response.ptr != nil && response.len > 0 {
		out = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.cliproxy_free_plugin_buffer(c.api.free_buffer, response.ptr, response.len)
	}
	if rc != 0 {
		if isPluginErrorEnvelope(out) {
			return out, nil
		}
		return nil, fmt.Errorf("plugin call %s returned %d: %s", method, int(rc), string(out))
	}
	return out, nil
}

func (c *dynamicLibraryClient) Shutdown() {
	c.close()
}

func (c *dynamicLibraryClient) closeAfterOpenFailure() {
	c.close()
}

// close 在逻辑上分离客户端，但不卸载插件映像。
// Go 插件库无法从宿主进程安全地热卸载：
// dlclose 之后插件运行时、其线程和信号处理器仍然常驻，
// 因此同一进程后续加载时会把信号转发到
// 未映射的内存并空转。初始化失败时也是如此，
// 因为 dlopen 可能已经启动了运行时。该插件是
// 从影子副本加载的，因此保持映像映射不会阻碍
// 删除或替换源产物。
func (c *dynamicLibraryClient) close() {
	if c == nil {
		return
	}
	if c.host != nil {
		c.host.closeHostHTTPCallbackInstance(c.pluginID, c.instance)
		c.host = nil
	}
	if c.api.shutdown != nil {
		C.cliproxy_shutdown_plugin(c.api.shutdown)
		c.api.shutdown = nil
	}
	c.api.call = nil
	c.api.free_buffer = nil
	if c.hostCtx != nil {
		// 移除回调注册，使反复重载不会
		// 永久固定宿主。C host context 和 host API 块本身
		// 被有意泄漏：常驻的插件运行时可能仍会把
		// 该 context 传回 cliproxyHostCall，后者在查找失败前
		// 会从中读取回调 id。
		id := uintptr(*(*C.uintptr_t)(c.hostCtx))
		hostCallbackEntries.Delete(id)
	}
}

func dlerrorString() string {
	errText := C.cliproxy_dlerror()
	if errText == nil {
		return ""
	}
	return C.GoString(errText)
}
