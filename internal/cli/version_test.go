package cli

import (
	"runtime/debug"
	"testing"
)

func TestCurrentVersionMetadataUsesInjectedReleaseMetadata(t *testing.T) {
	setVersionTestState(t)
	releaseMetadata = "oxpio-release-metadata:1.2.3|abcdef123456|2023-11-14T22:13:20Z|release"

	if got, want := formatVersion(), "oxpio version=1.2.3 commit=abcdef123456 date=2023-11-14T22:13:20Z type=release"; got != want {
		t.Fatalf("formatVersion() = %q, want %q", got, want)
	}
}

func TestCurrentVersionMetadataUsesInjectedReleaseValues(t *testing.T) {
	setVersionTestState(t)
	releaseVersion = "1.2.3"
	releaseCommit = "abcdef123456"
	releaseDateEpoch = "1700000000"
	releaseBuildType = "release"

	if got, want := formatVersion(), "oxpio version=1.2.3 commit=abcdef123456 date=2023-11-14T22:13:20Z type=release"; got != want {
		t.Fatalf("formatVersion() = %q, want %q", got, want)
	}
}

func TestCurrentVersionMetadataReportsSnapshotType(t *testing.T) {
	setVersionTestState(t)
	releaseVersion = "0.0.0-snapshot"
	releaseBuildType = "snapshot"

	if got, want := formatVersion(), "oxpio version=0.0.0-snapshot commit=unknown date=unknown type=snapshot"; got != want {
		t.Fatalf("formatVersion() = %q, want %q", got, want)
	}
}

func TestCurrentVersionMetadataUsesGoInstallBuildInfo(t *testing.T) {
	setVersionTestState(t)
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Path: "github.com/simp-lee/oxpio", Version: "v1.2.3"},
		}, true
	}

	if got, want := formatVersion(), "oxpio version=v1.2.3 commit=unknown date=unknown type=go-install"; got != want {
		t.Fatalf("formatVersion() = %q, want %q", got, want)
	}
}

func TestCurrentVersionMetadataLabelsVCSBuildAsDevelopment(t *testing.T) {
	setVersionTestState(t)
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Version: "v1.2.3-0.20260406073000-0123456789ab"},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "0123456789abcdef"},
				{Key: "vcs.time", Value: "2026-04-06T09:30:00+02:00"},
				{Key: "vcs.modified", Value: "false"},
			},
		}, true
	}

	if got, want := formatVersion(), "oxpio version=dev commit=0123456789abcdef date=2026-04-06T07:30:00Z type=dev"; got != want {
		t.Fatalf("formatVersion() = %q, want %q", got, want)
	}
}

func TestCurrentVersionMetadataLabelsDevelopmentBuildWithoutVCS(t *testing.T) {
	setVersionTestState(t)
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true
	}
	if got, want := formatVersion(), "oxpio version=dev commit=unknown date=unknown type=dev"; got != want {
		t.Fatalf("formatVersion() = %q, want %q", got, want)
	}
}

func setVersionTestState(t *testing.T) {
	t.Helper()
	oldVersion := releaseVersion
	oldCommit := releaseCommit
	oldDate := releaseDateEpoch
	oldType := releaseBuildType
	oldMetadata := releaseMetadata
	oldRead := readBuildInfo
	releaseVersion = ""
	releaseCommit = ""
	releaseDateEpoch = ""
	releaseBuildType = ""
	releaseMetadata = ""
	readBuildInfo = func() (*debug.BuildInfo, bool) { return nil, false }
	t.Cleanup(func() {
		releaseVersion = oldVersion
		releaseCommit = oldCommit
		releaseDateEpoch = oldDate
		releaseBuildType = oldType
		releaseMetadata = oldMetadata
		readBuildInfo = oldRead
	})
}
