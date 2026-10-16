package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/discover"
)

// First run and configuration from the GUI: the prerequisites, the AWS
// profiles and SQL Server instances, and the writes of the targets. Every
// write goes through config.Update: atomic, 0600, never over a file that
// does not parse, and never with a password (only where to find it).

func init() { extraRoutes = append(extraRoutes, (*Server).firstRunRoutes) }

func (s *Server) firstRunRoutes() []route {
	return []route{
		{http.MethodGet, "/api/v1/prerequisites", s.prerequisites},
		{http.MethodGet, "/api/v1/discovery/profiles", s.discoveryProfiles},
		{http.MethodGet, "/api/v1/discovery/instances", s.discoveryInstances},
		{http.MethodPost, "/api/v1/config/targets", s.saveTarget},
		{http.MethodDelete, "/api/v1/config/targets/{name}", s.removeTarget},
	}
}

// discoveryTimeout bounds a discovery: STS, then RDS once per option group.
const discoveryTimeout = 60 * time.Second

// PrerequisiteList is the answer of GET /api/v1/prerequisites.
type PrerequisiteList struct {
	Prerequisites []discover.Prerequisite `json:"prerequisites"`
}

// ProfileList is the answer of GET /api/v1/discovery/profiles.
type ProfileList struct {
	Profiles []discover.Profile `json:"profiles"`
}

// TargetInput is the body of POST /api/v1/config/targets: a target as the
// GUI form fills it. PasswordSource says where the SQL password comes from
// (prompt or env:VAR), never the password.
type TargetInput struct {
	Name           string `json:"name"`
	AccountID      string `json:"accountId"`
	Region         string `json:"region"`
	Profile        string `json:"profile"`
	Instance       string `json:"instance"`
	Bucket         string `json:"bucket"`
	SQLUser        string `json:"sqlUser"`
	PasswordSource string `json:"passwordSource"`
	ConnectMode    string `json:"connectMode"`
	LocalPort      int    `json:"localPort,omitempty"`
	Protected      bool   `json:"protected"`
	// Replace changes the target with the same name (409 without it). The
	// keys the form does not show (prefixes, max_tasks, bridge_tag) keep
	// their values.
	Replace bool `json:"replace,omitempty"`
}

// apply sets the fields of the form on base.
func (in TargetInput) apply(base config.Target) config.Target {
	base.AccountID = in.AccountID
	base.Region = in.Region
	base.AWSProfile = in.Profile
	base.Instance = in.Instance
	base.Bucket = in.Bucket
	base.SQL = config.SQL{User: in.SQLUser, Password: in.PasswordSource}
	base.Connect.Mode = in.ConnectMode
	base.Connect.LocalPort = in.LocalPort
	base.Protected = in.Protected
	return base
}

func (s *Server) discovery() discover.Service {
	if s.opts.Discover != nil {
		return s.opts.Discover
	}
	return discover.System{Getenv: s.opts.Getenv}
}

func (s *Server) prerequisites(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, PrerequisiteList{Prerequisites: s.discovery().Prerequisites(r.Context())})
}

func (s *Server) discoveryProfiles(w http.ResponseWriter, _ *http.Request) {
	profiles, err := s.discovery().Profiles()
	if err != nil {
		http.Error(w, "AWS profiles: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, ProfileList{Profiles: profiles})
}

func (s *Server) discoveryInstances(w http.ResponseWriter, r *http.Request) {
	profile, region := r.URL.Query().Get("profile"), r.URL.Query().Get("region")
	if !config.ValidProfile(profile) {
		http.Error(w, fmt.Sprintf("invalid AWS profile %q", profile), http.StatusBadRequest)
		return
	}
	if !config.ValidRegion(region) {
		http.Error(w, fmt.Sprintf("invalid AWS region %q", region), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), discoveryTimeout)
	defer cancel()
	list, err := s.discovery().Instances(ctx, profile, region)
	if err != nil {
		if r.Context().Err() == nil {
			s.opts.Log.Warn("discovery failed", "profile", profile, "region", region, "err", err)
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, list)
}

// saveTarget adds a target to the configuration file, or replaces it with
// replace: 201 added, 200 replaced, 400 invalid, 409 the name exists or the
// file on disk does not parse.
func (s *Server) saveTarget(w http.ResponseWriter, r *http.Request) {
	var in TargetInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := config.SupportedPasswordSource(in.PasswordSource); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := config.Check(in.Name, in.apply(config.Target{})); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var saved config.Target
	replaced := false
	err := config.Update(s.opts.ConfigPath, func(c *config.Config) error {
		base, exists := c.Targets[in.Name]
		if err := c.SetTarget(in.Name, in.apply(base), in.Replace); err != nil {
			return err
		}
		saved, replaced = c.Targets[in.Name], exists
		return nil
	})
	var invalid *config.InvalidFileError
	switch {
	case errors.Is(err, config.ErrTargetExists):
		http.Error(w, err.Error()+": choose another name, or replace it", http.StatusConflict)
		return
	case errors.As(err, &invalid):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		s.opts.Log.Error("save target", "target", in.Name, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.opts.Log.Info("target saved", "target", in.Name, "instance", saved.Instance, "replaced", replaced)
	status := http.StatusCreated
	if replaced {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(s.summaryOf(in.Name, saved))
}

// removeTarget deletes a target: ?confirm= must repeat its name. 204
// removed, 400 no confirmation, 404 unknown, 409 the file does not parse.
func (s *Server) removeTarget(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if r.URL.Query().Get("confirm") != name {
		http.Error(w, "confirm must repeat the target name", http.StatusBadRequest)
		return
	}
	err := config.Update(s.opts.ConfigPath, func(c *config.Config) error { return c.RemoveTarget(name) })
	var invalid *config.InvalidFileError
	switch {
	case errors.Is(err, config.ErrUnknownTarget):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case errors.As(err, &invalid):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		s.opts.Log.Error("remove target", "target", name, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.opts.Passwords.ForgetMemory(name)
	s.opts.Log.Info("target removed", "target", name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) summaryOf(name string, t config.Target) TargetSummary {
	return TargetSummary{
		Password: s.passwordInfo(name, t),
		Name:     name, Instance: t.Instance, Region: t.Region, Profile: t.AWSProfile,
		AccountID: t.AccountID, Bucket: t.Bucket, SQLUser: t.SQL.User,
		ConnectMode: t.Connect.Mode, LocalPort: t.Connect.LocalPort, Protected: t.Protected,
		ImportPrefix: t.Prefixes.Import, ExportPrefix: t.Prefixes.Export,
	}
}
