package model

import "time"

type Probe struct {
	URL            string `json:"url"`
	Statuses       []int  `json:"statuses"`
	MinBytes       int64  `json:"min_bytes,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}
type Candidate struct {
	Tag      string `json:"tag"`
	ProxyURL string `json:"proxy_url"`
	Priority int    `json:"priority"`
	Cost     int    `json:"cost,omitempty"`
	Tunnel   string `json:"tunnel,omitempty"`
}
type Policy struct {
	Selector    string      `json:"selector"`
	Candidates  []Candidate `json:"candidates"`
	Probes      []Probe     `json:"probes"`
	Failures    int         `json:"failures"`
	Recoveries  int         `json:"recoveries"`
	HoldSeconds int         `json:"hold_seconds"`
}
type Tunnel struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Listen   string `json:"listen"`
	Binary   string `json:"binary"`
}
type Config struct {
	Listen           string   `json:"listen"`
	Token            string   `json:"token"`
	Controller       string   `json:"controller"`
	ControllerSecret string   `json:"controller_secret,omitempty"`
	IntervalSeconds  int      `json:"interval_seconds"`
	Policies         []Policy `json:"policies"`
	Tunnels          []Tunnel `json:"tunnels,omitempty"`
}
type Result struct {
	URL        string `json:"url"`
	Status     int    `json:"status"`
	Bytes      int64  `json:"bytes"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}
type State struct {
	Tag       string    `json:"tag"`
	Healthy   bool      `json:"healthy"`
	Failures  int       `json:"failures"`
	Successes int       `json:"successes"`
	CheckedAt time.Time `json:"checked_at"`
	Results   []Result  `json:"results"`
}
