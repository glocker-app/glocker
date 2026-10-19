// Build metadata surfaced so you can tell which build is running (toolbar-icon
// tooltip + a one-line startup log). The real commit is stamped in at sign time
// by `make sign-extension`; "dev" is the value for an unsigned/`web-ext run` build.
var GLOCKER_BUILD = { commit: "dev" };
