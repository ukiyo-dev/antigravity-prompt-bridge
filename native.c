#include "native.h"
#include <stdlib.h>

#if defined(__APPLE__) && defined(__x86_64__)
#include <pthread.h>

/* Intel macOS Go runtimes can alias the Go TLS slot when one Go runtime
 * enters another on a host-owned thread (Go issue #38692). Start each entry
 * on a fresh native thread. Join before returning: the ABI is synchronous
 * and the host owns the request pointer for the duration of the call only.
 */
enum bridge_op { BRIDGE_INIT, BRIDGE_CALL };
struct bridge_job {
    enum bridge_op op;
    cliproxy_host_api *host;
    cliproxy_plugin_api *plugin;
    char *method;
    void *data;
    size_t len;
    cliproxy_buffer *response;
    int result;
};

static void *bridge_worker(void *opaque) {
    struct bridge_job *job = opaque;
    switch (job->op) {
    case BRIDGE_INIT:
        job->result = cliproxyPluginInit(job->host, job->plugin);
        break;
    case BRIDGE_CALL:
        job->result = cliproxyPluginCall(job->method, job->data, job->len, job->response);
        break;
    }
    return NULL;
}

static int bridge_dispatch(struct bridge_job *job) {
    pthread_t thread;
    if (pthread_create(&thread, NULL, bridge_worker, job) != 0) {
        return 1;
    }
    /* This thread is joinable and distinct from the caller. */
    pthread_join(thread, NULL);
    return job->result;
}

static int bridge_call(char *method, uint8_t *data, size_t len, cliproxy_buffer *response) {
    struct bridge_job job = { .op = BRIDGE_CALL, .method = method, .data = data, .len = len, .response = response };
    if (response != NULL) {
        response->ptr = NULL;
        response->len = 0;
    }
    return bridge_dispatch(&job);
}

static void bridge_free(void *ptr, size_t len) { (void)len; free(ptr); }
static void bridge_shutdown(void) {}
#endif

#if defined(_WIN32)
__declspec(dllexport)
#else
__attribute__((visibility("default")))
#endif
int cliproxy_plugin_init(cliproxy_host_api *host, cliproxy_plugin_api *plugin) {
#if defined(__APPLE__) && defined(__x86_64__)
    struct bridge_job job = { .op = BRIDGE_INIT, .host = host, .plugin = plugin };
    int result = bridge_dispatch(&job);
    if (result == 0) {
        plugin->call = bridge_call;
        plugin->free_buffer = bridge_free;
        plugin->shutdown = bridge_shutdown;
    }
    return result;
#else
    return cliproxyPluginInit(host, plugin);
#endif
}
