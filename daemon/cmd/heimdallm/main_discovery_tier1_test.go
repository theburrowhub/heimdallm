package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/heimdallm/daemon/internal/scheduler"
)

type fakeTier1Discovery struct {
	refreshCalls int
	refreshTopic string
	refreshOrgs  []string
	refreshRepos []string
	refreshErr   error
	repos        []string
}

func (f *fakeTier1Discovery) Discovered() []string {
	if len(f.repos) == 0 {
		return nil
	}
	out := make([]string, len(f.repos))
	copy(out, f.repos)
	return out
}

func (f *fakeTier1Discovery) Refresh(topic string, orgs []string) error {
	f.refreshCalls++
	f.refreshTopic = topic
	f.refreshOrgs = append([]string(nil), orgs...)
	if f.refreshErr == nil {
		f.repos = append([]string(nil), f.refreshRepos...)
	}
	return f.refreshErr
}

type fakeTier1Publisher struct {
	calls int
	repos []string
}

func (f *fakeTier1Publisher) PublishRepos(_ context.Context, repos []string) error {
	f.calls++
	f.repos = append([]string(nil), repos...)
	return nil
}

func TestSendDiscoveryReposRefreshesTopicBeforePublish(t *testing.T) {
	disc := &fakeTier1Discovery{
		refreshRepos: []string{"org/discovered", "org/static", "org/ignored"},
	}
	pub := &fakeTier1Publisher{}

	sendDiscoveryRepos(
		context.Background(),
		disc,
		scheduler.NewRateLimiter(1),
		pub,
		func() scheduler.Tier1Config {
			return scheduler.Tier1Config{
				StaticRepos:    []string{"org/static"},
				NonMonitored:   []string{"org/ignored"},
				DiscoveryTopic: "heimdallm-review",
				DiscoveryOrgs:  []string{"org"},
			}
		},
	)

	if disc.refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", disc.refreshCalls)
	}
	if disc.refreshTopic != "heimdallm-review" {
		t.Errorf("refresh topic = %q, want heimdallm-review", disc.refreshTopic)
	}
	if !reflect.DeepEqual(disc.refreshOrgs, []string{"org"}) {
		t.Errorf("refresh orgs = %v, want [org]", disc.refreshOrgs)
	}
	want := []string{"org/static", "org/discovered"}
	if !reflect.DeepEqual(pub.repos, want) {
		t.Errorf("published repos = %v, want %v", pub.repos, want)
	}
}

func TestSendDiscoveryReposSkipsRefreshWhenTopicDisabled(t *testing.T) {
	disc := &fakeTier1Discovery{
		repos:        []string{"org/cached"},
		refreshRepos: []string{"org/discovered"},
	}
	pub := &fakeTier1Publisher{}

	sendDiscoveryRepos(
		context.Background(),
		disc,
		scheduler.NewRateLimiter(1),
		pub,
		func() scheduler.Tier1Config {
			return scheduler.Tier1Config{
				StaticRepos: []string{"org/static"},
			}
		},
	)

	if disc.refreshCalls != 0 {
		t.Fatalf("refresh calls = %d, want 0", disc.refreshCalls)
	}
	want := []string{"org/static"}
	if !reflect.DeepEqual(pub.repos, want) {
		t.Errorf("published repos = %v, want %v", pub.repos, want)
	}
}

func TestSendDiscoveryReposPublishesCachedReposWhenRefreshFails(t *testing.T) {
	disc := &fakeTier1Discovery{
		repos:      []string{"org/cached"},
		refreshErr: errors.New("rate limited"),
	}
	pub := &fakeTier1Publisher{}

	sendDiscoveryRepos(
		context.Background(),
		disc,
		scheduler.NewRateLimiter(1),
		pub,
		func() scheduler.Tier1Config {
			return scheduler.Tier1Config{
				StaticRepos:    []string{"org/static"},
				DiscoveryTopic: "heimdallm-review",
				DiscoveryOrgs:  []string{"org"},
			}
		},
	)

	if disc.refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", disc.refreshCalls)
	}
	want := []string{"org/static", "org/cached"}
	if !reflect.DeepEqual(pub.repos, want) {
		t.Errorf("published repos = %v, want %v", pub.repos, want)
	}
}
