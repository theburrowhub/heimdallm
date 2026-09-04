package server

import (
	"net/http"
	"time"

	"github.com/heimdallm/daemon/internal/instances"
)

// discoveredResponse is the shape of both discovery routes.
//
// Enabled is carried explicitly so the GUI can tell "discovery is switched off"
// from "discovery is on and found nothing". Those look identical in an empty
// list and want completely different copy: one is a setting to change, the
// other is a network to check.
type discoveredResponse struct {
	Enabled  bool                  `json:"enabled"`
	LastScan *time.Time            `json:"last_scan,omitempty"`
	Peers    []instances.Candidate `json:"peers"`
}

func (srv *Server) discoverer() *instances.Discoverer {
	deps := srv.clusterDeps()
	if deps == nil {
		return nil
	}
	return deps.Discoverer
}

// handleListDiscovered returns the cached view of the network.
//
// Cached rather than live: an mDNS browse has to sit and listen for a couple of
// seconds before it can claim to have heard everyone, and making the Instances
// tab wait on that every time it loads would be a poor trade for data that
// changes when someone plugs in a new machine.
func (srv *Server) handleListDiscovered(w http.ResponseWriter, r *http.Request) {
	d := srv.discoverer()
	if d == nil {
		writeJSON(w, http.StatusOK, discoveredResponse{Peers: []instances.Candidate{}})
		return
	}
	writeJSON(w, http.StatusOK, buildDiscoveredResponse(d, d.Candidates()))
}

// handleScanDiscovered forces a browse and returns what it found. This is the
// refresh button: the operator has just plugged a machine in and does not want
// to wait for the next tick.
func (srv *Server) handleScanDiscovered(w http.ResponseWriter, r *http.Request) {
	d := srv.discoverer()
	if d == nil {
		writeJSON(w, http.StatusOK, discoveredResponse{Peers: []instances.Candidate{}})
		return
	}
	writeJSON(w, http.StatusOK, buildDiscoveredResponse(d, d.Scan(r.Context())))
}

func buildDiscoveredResponse(d *instances.Discoverer, peers []instances.Candidate) discoveredResponse {
	if peers == nil {
		peers = []instances.Candidate{}
	}
	resp := discoveredResponse{Enabled: true, Peers: peers}
	if last := d.LastScan(); !last.IsZero() {
		utc := last.UTC()
		resp.LastScan = &utc
	}
	return resp
}
