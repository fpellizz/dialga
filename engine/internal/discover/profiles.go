// Package discover finds what a first run needs, read-only: the AWS profiles
// of this computer, the RDS SQL Server instances a profile can see, and the
// programs Dialga relies on (session-manager-plugin, podman or docker).
package discover

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// Profile is an AWS profile of the shared files.
type Profile struct {
	Name string `json:"name"`
	// Region is the profile's region in the config file, if it has one.
	Region string `json:"region,omitempty"`
}

// ProfileFiles returns the shared config and credentials files the SDK
// reads: $AWS_CONFIG_FILE and $AWS_SHARED_CREDENTIALS_FILE, or the defaults
// (~/.aws/config, ~/.aws/credentials).
func ProfileFiles(getenv func(string) string) (configFile, credentialsFile string) {
	configFile = getenv("AWS_CONFIG_FILE")
	if configFile == "" {
		configFile = awsconfig.DefaultSharedConfigFilename()
	}
	credentialsFile = getenv("AWS_SHARED_CREDENTIALS_FILE")
	if credentialsFile == "" {
		credentialsFile = awsconfig.DefaultSharedCredentialsFilename()
	}
	return configFile, credentialsFile
}

// Profiles lists the profiles of the two files, sorted by name. Of the
// credentials file only the section headers are read: its keys are skipped
// unread. The config file also gives each profile's region. A missing file
// is no profile, not an error.
func Profiles(configFile, credentialsFile string) ([]Profile, error) {
	regions := map[string]string{}
	if err := readSections(configFile, true, regions); err != nil {
		return nil, err
	}
	if err := readSections(credentialsFile, false, regions); err != nil {
		return nil, err
	}
	list := make([]Profile, 0, len(regions))
	for name, region := range regions {
		list = append(list, Profile{Name: name, Region: region})
	}
	slices.SortFunc(list, func(a, b Profile) int { return strings.Compare(a.Name, b.Name) })
	return list, nil
}

func readSections(path string, isConfig bool, regions map[string]string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return parseSections(f, isConfig, regions)
}

// parseSections adds the profiles of one shared file to regions. In the
// config file a profile is [default] or [profile name] (other sections, like
// [sso-session x] or [services x], are not profiles) and its region is a
// top-level "region" key. In the credentials file every [name] is a profile
// and nothing else is looked at.
func parseSections(r io.Reader, isConfig bool, regions map[string]string) error {
	current := "" // profile of the section being read, "" outside profiles
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				current = ""
				continue
			}
			current = profileName(strings.TrimSpace(line[1:end]), isConfig)
			if _, seen := regions[current]; current != "" && !seen {
				regions[current] = ""
			}
			continue
		}
		if !isConfig || current == "" || raw[0] == ' ' || raw[0] == '\t' {
			continue // credentials keys, keys of other sections, sub-properties
		}
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "region" {
			regions[current] = strings.TrimSpace(value)
		}
	}
	return scanner.Err()
}

func profileName(section string, isConfig bool) string {
	if !isConfig {
		return section
	}
	if section == "default" {
		return section
	}
	if name, ok := strings.CutPrefix(section, "profile "); ok {
		return strings.TrimSpace(name)
	}
	return ""
}
