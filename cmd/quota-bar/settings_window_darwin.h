#ifndef QUOTA_SETTINGS_WINDOW_DARWIN_H
#define QUOTA_SETTINGS_WINDOW_DARWIN_H

#include <stdint.h>

void quota_settings_open(uint64_t token, const char *snapshot_json);
void quota_settings_complete(uint64_t token, const char *error_message);
void quotaSettingsApply(uint64_t token, char *edited_json);
void quotaSettingsClosed(uint64_t token);

#endif
