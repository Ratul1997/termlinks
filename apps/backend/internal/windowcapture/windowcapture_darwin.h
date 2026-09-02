#ifndef TERMLINKS_WINDOW_CAPTURE_H
#define TERMLINKS_WINDOW_CAPTURE_H

#include <stddef.h>
#include <stdint.h>

int tl_window_supported(void);
int tl_screen_recording_allowed(void);
int tl_accessibility_allowed(void);
void tl_request_permissions(void);
int tl_window_sources(char **json, char **error);
void *tl_window_open(uint32_t window_id, int max_width, int max_height, char **error);
int tl_window_frame(void *capture, unsigned char **data, size_t *length, int *width, int *height, char **error);
int tl_window_pointer(void *capture, const char *action, double x, double y, int button, double delta_x, double delta_y);
int tl_window_key(void *capture, const char *code, int down, int shift, int control, int option, int command);
int tl_window_text(void *capture, const char *text);
int tl_window_clipboard(void *capture, const char *text);
void tl_window_close(void *capture);

#endif
