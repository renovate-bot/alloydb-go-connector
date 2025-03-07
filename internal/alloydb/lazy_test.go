// Copyright 2024 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package alloydb

import (
	"context"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/alloydbconn/internal/mock"
	"google.golang.org/api/option"

	alloydbadmin "cloud.google.com/go/alloydb/apiv1alpha"
	telv2 "cloud.google.com/go/alloydbconn/internal/tel/v2"
)

func TestLazyRefreshCacheConnectionInfo(t *testing.T) {
	u := testInstanceURI()
	inst := mock.NewFakeInstance(u.project, u.region, u.cluster, u.name)
	client, url, cleanup := mock.HTTPClient(
		mock.InstanceGetSuccess(inst, 1),
		mock.CreateEphemeralSuccess(inst, 1),
	)
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("%v", err)
		}
	}()
	ctx := context.Background()
	c, err := alloydbadmin.NewAlloyDBAdminRESTClient(
		ctx,
		option.WithHTTPClient(client),
		option.WithEndpoint(url),
		option.WithTokenSource(stubTokenSource{}),
	)
	if err != nil {
		t.Fatalf("expected NewClient to succeed, but got error: %v", err)
	}
	cache := NewLazyRefreshCache(
		testInstanceURI(), nullLogger{}, c,
		rsaKey, 30*time.Second, "",
		false,
		"some-ua",
		telv2.NullMetricRecorder{},
	)

	ci, err := cache.ConnectionInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ci.Instance != u {
		t.Fatalf("want = %v, got = %v", u, ci.Instance)
	}
	// Request connection info again to ensure it uses the cache and doesn't
	// send another API call.
	_, err = cache.ConnectionInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func TestLazyRefreshCacheForceRefresh(t *testing.T) {
	u := testInstanceURI()
	inst := mock.NewFakeInstance(u.project, u.region, u.cluster, u.name)
	client, url, cleanup := mock.HTTPClient(
		mock.InstanceGetSuccess(inst, 2),
		mock.CreateEphemeralSuccess(inst, 2),
	)
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("%v", err)
		}
	}()
	ctx := context.Background()
	c, err := alloydbadmin.NewAlloyDBAdminRESTClient(
		ctx,
		option.WithHTTPClient(client),
		option.WithEndpoint(url),
		option.WithTokenSource(stubTokenSource{}),
	)
	if err != nil {
		t.Fatalf("expected NewClient to succeed, but got error: %v", err)
	}
	cache := NewLazyRefreshCache(
		testInstanceURI(), nullLogger{}, c,
		rsaKey, 30*time.Second, "",
		false,
		"some-ua",
		telv2.NullMetricRecorder{},
	)

	_, err = cache.ConnectionInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	cache.ForceRefresh()

	_, err = cache.ConnectionInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

type mockMetricRecorder struct {
	mu       sync.Mutex
	gotAttrs telv2.Attributes

	telv2.MetricRecorder
}

func (m *mockMetricRecorder) RecordRefreshCount(_ context.Context, a telv2.Attributes) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gotAttrs = a
}

func (m *mockMetricRecorder) Verify(t *testing.T, wantAttrs telv2.Attributes) {
	for range 10 {
		m.mu.Lock()
		gotAttrs := m.gotAttrs
		m.mu.Unlock()
		if gotAttrs == wantAttrs {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("got = %v, want = %v", m.gotAttrs, wantAttrs)
}

func TestLazyRefreshCacheMetrics(t *testing.T) {
	u := testInstanceURI()
	inst := mock.NewFakeInstance(u.project, u.region, u.cluster, u.name)
	tcs := []struct {
		desc      string
		requests  []*mock.Request
		wantAttrs telv2.Attributes
	}{
		{
			desc: "refresh count success",
			requests: []*mock.Request{
				mock.InstanceGetSuccess(inst, 1),
				mock.CreateEphemeralSuccess(inst, 1),
			},
			wantAttrs: telv2.Attributes{
				UserAgent:     "some-ua",
				RefreshType:   "lazy",
				RefreshStatus: "success",
			},
		},
		{
			desc:     "refresh count success",
			requests: []*mock.Request{}, // no requests will result in 500s
			wantAttrs: telv2.Attributes{
				UserAgent:     "some-ua",
				RefreshType:   "lazy",
				RefreshStatus: "failure",
			},
		},
	}

	for _, tc := range tcs {
		t.Run(tc.desc, func(t *testing.T) {
			client, url, cleanup := mock.HTTPClient(tc.requests...)
			defer func() {
				if err := cleanup(); err != nil {
					t.Fatalf("%v", err)
				}
			}()
			ctx := context.Background()
			c, err := alloydbadmin.NewAlloyDBAdminRESTClient(
				ctx,
				option.WithHTTPClient(client),
				option.WithEndpoint(url),
				option.WithTokenSource(stubTokenSource{}),
			)
			if err != nil {
				t.Fatalf("expected NewClient to succeed, but got error: %v", err)
			}
			defer c.Close()

			mockRecorder := &mockMetricRecorder{}
			cache := NewLazyRefreshCache(
				testInstanceURI(), nullLogger{}, c,
				rsaKey, 30*time.Second, "",
				false,
				"some-ua",
				mockRecorder,
			)

			cache.ConnectionInfo(context.Background())

			mockRecorder.Verify(t, tc.wantAttrs)
		})
	}
}
