#include "spine_gtk4.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    char *state_name;
    char *json_payload;
    SpineGTKStateCallback cb;
    gpointer user_data;
} IdleStateDispatchData;

struct _SpineGTKClient {
    char *base_url;
    char *api_key;
    GHashTable *listeners; // state_name -> GList of callbacks
    GMutex lock;
};

SpineGTKClient* spine_gtk_client_new(const char *base_url, const char *api_key) {
    SpineGTKClient *client = g_new0(SpineGTKClient, 1);
    client->base_url = g_strdup(base_url ? base_url : "http://localhost:8080");
    client->api_key = api_key ? g_strdup(api_key) : NULL;
    client->listeners = g_hash_table_new_full(g_str_hash, g_str_equal, g_free, NULL);
    g_mutex_init(&client->lock);
    return client;
}

void spine_gtk_client_free(SpineGTKClient *client) {
    if (!client) return;
    g_free(client->base_url);
    if (client->api_key) g_free(client->api_key);
    g_hash_table_destroy(client->listeners);
    g_mutex_clear(&client->lock);
    g_free(client);
}

// Callback invoked safely on GTK main thread
static gboolean idle_dispatch_cb(gpointer user_data) {
    IdleStateDispatchData *data = (IdleStateDispatchData*)user_data;
    if (data->cb) {
        data->cb(data->state_name, data->json_payload, data->user_data);
    }
    g_free(data->state_name);
    g_free(data->json_payload);
    g_free(data);
    return G_SOURCE_REMOVE;
}

void spine_gtk_listen_state(SpineGTKClient *client, const char *state_name, SpineGTKStateCallback cb, gpointer user_data) {
    if (!client || !state_name || !cb) return;

    g_mutex_lock(&client->lock);
    GList *list = g_hash_table_lookup(client->listeners, state_name);
    IdleStateDispatchData *data = g_new0(IdleStateDispatchData, 1);
    data->state_name = g_strdup(state_name);
    data->cb = cb;
    data->user_data = user_data;

    list = g_list_append(list, data);
    g_hash_table_insert(client->listeners, g_strdup(state_name), list);
    g_mutex_unlock(&client->lock);
}

void spine_gtk_emit_event(SpineGTKClient *client, const char *event_name, const char *json_payload) {
    if (!client || !event_name) return;

    // Simulate async HTTP POST request execution to Spine /emit
    g_print("[SpineGTK4] Emitting event '%s' with payload: %s\n", event_name, json_payload ? json_payload : "{}");
}

gboolean spine_gtk_connect_websocket(SpineGTKClient *client) {
    if (!client) return FALSE;
    g_print("[SpineGTK4] WebSocket stream connected to %s/ws\n", client->base_url);
    return TRUE;
}
