#ifndef SPINE_GTK4_H
#define SPINE_GTK4_H

#include <gtk/gtk.h>
#include <glib.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct _SpineGTKClient SpineGTKClient;

// Callback function type triggered on the GTK main loop when Spine emits a state update
typedef void (*SpineGTKStateCallback)(const char *state_name, const char *json_payload, gpointer user_data);

// Initializes a Spine GTK4 client context connected to the specified base URL
SpineGTKClient* spine_gtk_client_new(const char *base_url, const char *api_key);

// Frees the Spine GTK4 client context
void spine_gtk_client_free(SpineGTKClient *client);

// Asynchronously emits an event to the Spine server from GTK4 UI event handlers
void spine_gtk_emit_event(SpineGTKClient *client, const char *event_name, const char *json_payload);

// Registers a state listener callback that executes safely on the GTK4 main thread
void spine_gtk_listen_state(SpineGTKClient *client, const char *state_name, SpineGTKStateCallback cb, gpointer user_data);

// Connects to Spine WebSocket state broadcast stream and starts GTK4 main loop dispatch
gboolean spine_gtk_connect_websocket(SpineGTKClient *client);

#ifdef __cplusplus
}
#endif

#endif // SPINE_GTK4_H
