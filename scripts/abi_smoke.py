#!/usr/bin/env python3
"""Exercise the built shared library's real CPA C ABI with local fake callbacks."""
import base64
import ctypes as C
import json
import pathlib
import sys
import tempfile


class Buffer(C.Structure):
    _fields_ = [("ptr", C.c_void_p), ("len", C.c_size_t)]


HostCall = C.CFUNCTYPE(C.c_int, C.c_void_p, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
Free = C.CFUNCTYPE(None, C.c_void_p, C.c_size_t)
PluginCall = C.CFUNCTYPE(C.c_int, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
Shutdown = C.CFUNCTYPE(None)


class HostAPI(C.Structure):
    _fields_ = [("abi_version", C.c_uint32), ("host_ctx", C.c_void_p), ("call", HostCall), ("free_buffer", Free)]


class PluginAPI(C.Structure):
    _fields_ = [("abi_version", C.c_uint32), ("call", PluginCall), ("free_buffer", Free), ("shutdown", Shutdown)]


def main():
    library = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "dist/cpa-model-dock.dylib").resolve()
    lib = C.CDLL(str(library))
    libc = C.CDLL(None)
    libc.malloc.argtypes, libc.malloc.restype = [C.c_size_t], C.c_void_p
    libc.free.argtypes = [C.c_void_p]

    @HostCall
    def host_call(_ctx, method, _request, _length, out):
        result = {b"host.auth.models": {"credentials": []}, b"host.auth.list": {"files": []}, b"host.log": {}}.get(method)
        value = {"ok": True, "result": result} if result is not None else {"ok": False, "error": {"code": "unsupported", "message": "unsupported fake callback"}}
        raw = json.dumps(value).encode()
        ptr = libc.malloc(len(raw))
        C.memmove(ptr, raw, len(raw))
        out[0] = Buffer(ptr, len(raw))
        return 0

    @Free
    def host_free(ptr, _length):
        libc.free(ptr)

    host, plugin = HostAPI(1, None, host_call, host_free), PluginAPI()
    lib.cliproxy_plugin_init.argtypes = [C.POINTER(HostAPI), C.POINTER(PluginAPI)]
    lib.cliproxy_plugin_init.restype = C.c_int
    assert lib.cliproxy_plugin_init(C.byref(host), C.byref(plugin)) == 0
    assert plugin.abi_version == 1

    def call(method, request):
        raw = json.dumps(request).encode()
        payload = C.create_string_buffer(raw)
        response = Buffer()
        assert plugin.call(method.encode(), payload, len(raw), C.byref(response)) == 0
        try:
            envelope = json.loads(C.string_at(response.ptr, response.len))
        finally:
            plugin.free_buffer(response.ptr, response.len)
        assert envelope["ok"], envelope
        return envelope.get("result")

    try:
        with tempfile.TemporaryDirectory(prefix="cpa-model-dock-abi-") as state_dir:
            config = base64.b64encode(f"state-dir: {state_dir}\n".encode()).decode()
            for schema in (6, 7):
                registration = call("plugin.register" if schema == 6 else "plugin.reconfigure", {"schema_version": schema, "config_yaml": config})
                assert registration["schema_version"] == schema
                assert registration["capabilities"]["executor"]
                assert not registration["capabilities"]["frontend_auth_provider_exclusive"]
            routes = call("management.register", {"BasePath": "/v0/management", "ResourceBasePath": "/v0/resource/plugins/cpa-model-dock"})
            assert len(routes["routes"]) == 11 and len(routes["resources"]) == 5
            state = {"revision": 0, "settings": {"whitelist_enabled": True, "rewrite_response_model": False, "refresh_interval_seconds": 300}, "families": [], "aliases": [], "rules": []}
            put = call("management.handle", {"Method": "PUT", "Path": "/v0/management/plugins/cpa-model-dock/state", "Body": base64.b64encode(json.dumps(state).encode()).decode()})
            assert put["StatusCode"] == 200, put
            denied = call("request.intercept_before", {"RequestID": "abi-smoke", "RequestedModel": "hidden", "Model": "hidden", "SourceFormat": "openai"})
            assert denied["Terminate"] and denied["StatusCode"] == 403, denied
            alpha = call("model.route", {"SourceFormat": "codex-alpha-search", "RequestedModel": "hidden", "AvailableProviders": ["codex"]})
            assert alpha["Handled"] and alpha["TargetKind"] == "self", alpha
            call("request.complete", {"RequestID": "abi-smoke"})
            call("plugin.quiesce", {})
    finally:
        plugin.shutdown()
    print("PASS: native init/call/free/shutdown, schemas 6/7, management PUT, policy denial, Alpha sentinel")


if __name__ == "__main__":
    main()
