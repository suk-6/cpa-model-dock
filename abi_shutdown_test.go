//go:build cgo && (darwin || linux || freebsd)

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Exercise the real exported ABI in a separate process: Go test files cannot
// import C, and a deadlocked shutdown must be killable without hanging the suite.
func TestNativeShutdownRetiresTableWithoutWaitingForCallback(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "dock.so")
	if goruntime.GOOS == "darwin" {
		lib = filepath.Join(dir, "dock.dylib")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-buildmode=c-shared", "-o", lib, ".")
	build.Env = append(os.Environ(), "CC=/usr/bin/clang", "CGO_ENABLED=1", "GOTOOLCHAIN=go1.26.10", "GOFLAGS=-mod=mod", "GOPROXY=off", "GOCACHE=/tmp/cpa-model-dock-go-build")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native fixture: %v\n%s", err, out)
	}
	source := filepath.Join(dir, "native.c")
	if err := os.WriteFile(source, []byte(nativeShutdownHarness), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "native")
	args := []string{"-pthread", source, "-o", binary}
	if goruntime.GOOS != "darwin" {
		args = append(args, "-ldl")
	}
	if out, err := exec.CommandContext(ctx, "/usr/bin/clang", args...).CombinedOutput(); err != nil {
		t.Fatalf("compile native fixture: %v\n%s", err, out)
	}
	request := func(method, path string, body []byte) string {
		return string(mustJSON(pluginapi.ManagementRequest{Method: method, Path: "/v0/management/plugins/cpa-model-dock" + path, Body: body}))
	}
	state := autoState()
	state.Aliases[0].Auto.HealthCheck = true
	registration := string(mustJSON(map[string]any{"schema_version": 7, "config_yaml": []byte("state-dir: " + filepath.Join(dir, "state") + "\n")}))
	runCtx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	run := exec.CommandContext(runCtx, binary, lib, registration, request("POST", "/catalog/refresh", []byte(`{}`)), request("PUT", "/state", mustJSON(state)), request("POST", "/aliases/check", []byte(`{"revision":1,"alias_id":"al_one"}`)))
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("native shutdown/late free: %v (deadline %v)\n%s", err, runCtx.Err(), out)
	}
}

const nativeShutdownHarness = `
#include <stdint.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <pthread.h>
#include <dlfcn.h>
typedef struct {void *ptr;size_t len;} buffer;
typedef struct {uint32_t abi_version;void *ctx;int (*call)(void*,const char*,const uint8_t*,size_t,buffer*);void (*free_buffer)(void*,size_t);} host_api;
typedef struct {uint32_t abi_version;int (*call)(char*,uint8_t*,size_t,buffer*);void (*free_buffer)(void*,size_t);void (*shutdown)(void);} plugin_api;
static plugin_api plugin;
static pthread_mutex_t mu=PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t cond=PTHREAD_COND_INITIALIZER;
static int entered,released,freed,retired;
static void *probe_buffer;
static const char *check_json;
static int respond(buffer*out,const char*json){out->len=strlen(json);out->ptr=malloc(out->len);memcpy(out->ptr,json,out->len);return 0;}
static int host_call(void*ctx,const char*method,const uint8_t*in,size_t n,buffer*out){
 if(!strcmp(method,"host.auth.models"))return respond(out,"{\"ok\":true,\"result\":{\"credentials\":[{\"id\":\"one\",\"auth_index\":\"idx\",\"provider\":\"codex\",\"models\":[\"gpt-6.1-sol\",\"gpt-6.2-sol\"]}]}}");
 if(!strcmp(method,"host.auth.list"))return respond(out,"{\"ok\":true,\"result\":{\"files\":[{\"id\":\"one\",\"auth_index\":\"idx\",\"status\":\"active\"}]}}");
 if(!strcmp(method,"host.auth.get_runtime"))return respond(out,"{\"ok\":true,\"result\":{\"auth\":{\"id\":\"one\",\"auth_index\":\"idx\",\"status\":\"active\"}}}");
 if(!strcmp(method,"host.model.execute")){
  pthread_mutex_lock(&mu);entered++;pthread_cond_broadcast(&cond);
  while(!released)pthread_cond_wait(&cond,&mu);
  respond(out,"{\"ok\":true,\"result\":{\"status_code\":200}}");probe_buffer=out->ptr;pthread_mutex_unlock(&mu);return 0;
 }
 return respond(out,"{\"ok\":true,\"result\":{}}");
}
static void host_free(void*p,size_t n){
 pthread_mutex_lock(&mu);if(p==probe_buffer){if(!retired)abort();freed++;pthread_cond_broadcast(&cond);}pthread_mutex_unlock(&mu);free(p);
}
static void invoke(const char*method,const char*json,int require200){
 buffer out={0};if(plugin.call((char*)method,(uint8_t*)json,strlen(json),&out))abort();
 char*s=malloc(out.len+1);memcpy(s,out.ptr,out.len);s[out.len]=0;
 if(!strstr(s,"\"ok\":true")||(require200&&!strstr(s,"\"StatusCode\":200"))){fprintf(stderr,"%s: %s\n",method,s);abort();}
 free(s);plugin.free_buffer(out.ptr,out.len);
}
static void*check(void*unused){invoke("management.handle",check_json,0);return NULL;}
int main(int argc,char**argv){
 if(argc!=6)return 2;void*image=dlopen(argv[1],RTLD_NOW|RTLD_LOCAL);if(!image){fprintf(stderr,"%s\n",dlerror());return 3;}
 int(*init)(host_api*,plugin_api*)=dlsym(image,"cliproxy_plugin_init");host_api host={1,NULL,host_call,host_free};if(!init||init(&host,&plugin))return 4;
 invoke("plugin.register",argv[2],0);invoke("management.handle",argv[3],1);invoke("management.handle",argv[4],1);
 check_json=argv[5];pthread_t thread;pthread_create(&thread,NULL,check,NULL);
 pthread_mutex_lock(&mu);while(!entered)pthread_cond_wait(&cond,&mu);pthread_mutex_unlock(&mu);
 plugin.shutdown(); // must return while host_call is still blocked
 host.call=NULL;host.free_buffer=NULL;host.ctx=NULL; // the callback/free use their table snapshot
 pthread_join(thread,NULL);
 pthread_mutex_lock(&mu);retired=1;released=1;pthread_cond_broadcast(&cond);while(!freed)pthread_cond_wait(&cond,&mu);if(entered!=1)abort();pthread_mutex_unlock(&mu);
 // Like CPA, retain image/table/context until process exit.
 return 0;
}
`
