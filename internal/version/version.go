package version

import (
	"runtime"
	"runtime/debug"
)

// These values are replaced by release builds through -ldflags. The debug
// build information fallback keeps `go install ...@vX.Y.Z` self-describing.
var (
	Version   = "dev"
	Commit    = "none"
	BuildTime = "unknown"
)

type Info struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Built    string `json:"built"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

func Current() Info {
	v, commit, built := Version, Commit, BuildTime
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		if v == "dev" && buildInfo.Main.Version != "" && buildInfo.Main.Version != "(devel)" {
			v = buildInfo.Main.Version
		}
		for _, setting := range buildInfo.Settings {
			switch {
			case setting.Key == "vcs.revision" && commit == "none" && len(setting.Value) >= 7:
				commit = setting.Value[:7]
			case setting.Key == "vcs.time" && built == "unknown":
				built = setting.Value
			}
		}
	}
	return Info{Version: v, Commit: commit, Built: built, Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
}
