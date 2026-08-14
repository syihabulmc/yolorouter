//go:build windows

package selfupdate

// ScheduleRestart is a no-op on windows: the update endpoint is never
// offered there (Mode returns ModeWindows, and Apply refuses), so this stub
// exists only to keep the package compiling for windows builds.
func ScheduleRestart() {}
