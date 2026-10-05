package cli

import (
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

var (
	releaseVersion   string
	releaseCommit    string
	releaseDateEpoch string
	releaseBuildType string
	releaseMetadata  string
	readBuildInfo    = debug.ReadBuildInfo
)

type versionMetadata struct {
	Version string
	Commit  string
	Date    string
	Type    string
}

const releaseMetadataPrefix = "oxpio-release-metadata:"

func currentVersionMetadata() versionMetadata {
	if metadata, ok := injectedVersionMetadata(); ok {
		return metadata
	}
	if version := strings.TrimSpace(releaseVersion); version != "" {
		buildType := strings.TrimSpace(releaseBuildType)
		if buildType == "" {
			buildType = "release"
		}
		return versionMetadata{
			Version: version,
			Commit:  knownValue(releaseCommit),
			Date:    normalizeBuildDate(releaseDateEpoch),
			Type:    buildType,
		}
	}

	metadata := versionMetadata{Version: "dev", Commit: "unknown", Date: "unknown", Type: "dev"}
	info, ok := readBuildInfo()
	if !ok || info == nil {
		return metadata
	}
	hasVCS := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			hasVCS = true
			metadata.Commit = knownValue(setting.Value)
		case "vcs.time":
			hasVCS = true
			metadata.Date = normalizeBuildDate(setting.Value)
		case "vcs.modified":
			hasVCS = true
		}
	}
	if !hasVCS {
		if version := strings.TrimSpace(info.Main.Version); version != "" && version != "(devel)" {
			metadata.Version = version
			metadata.Type = "go-install"
		}
	}
	return metadata
}

func injectedVersionMetadata() (versionMetadata, bool) {
	value := strings.TrimSpace(releaseMetadata)
	if !strings.HasPrefix(value, releaseMetadataPrefix) {
		return versionMetadata{}, false
	}
	fields := strings.Split(strings.TrimPrefix(value, releaseMetadataPrefix), "|")
	if len(fields) != 4 || strings.TrimSpace(fields[0]) == "" {
		return versionMetadata{}, false
	}
	buildType := strings.TrimSpace(fields[3])
	if buildType == "" {
		buildType = "release"
	}
	return versionMetadata{
		Version: strings.TrimSpace(fields[0]),
		Commit:  knownValue(fields[1]),
		Date:    normalizeBuildDate(fields[2]),
		Type:    buildType,
	}, true
}

func formatVersion() string {
	metadata := currentVersionMetadata()
	return "oxpio version=" + metadata.Version + " commit=" + metadata.Commit + " date=" + metadata.Date + " type=" + metadata.Type
}

func knownValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func normalizeBuildDate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	if epoch, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Unix(epoch, 0).UTC().Format(time.RFC3339)
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC().Format(time.RFC3339)
	}
	return "unknown"
}
